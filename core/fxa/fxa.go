// Package fxa implements Mozilla Firefox Accounts authentication:
// PBKDF2 + HKDF password stretching, HAWK request signing, session login
// (with email-2fa code verification, TOTP, and unblock codes), OAuth token
// exchange (fxa-credentials + PKCE fallback), and access token refresh.
package fxa

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

const (
	AuthServer    = "https://api.accounts.firefox.com/v1"
	OAuthTokenURL = "https://oauth.accounts.firefox.com/v1/token"
	FirefoxClientID = "5882386c6d801776"
	OAuthScope      = "profile https://identity.mozilla.com/apps/vpn"

	// Desktop Firefox User-Agent + Accept-Encoding: identity prevents HTTP 406 on FxA/OAuth
	FirefoxUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0"

	protocolVersion         = "identity.mozilla.com/picl/v1/"
	pbkdf2Rounds            = 1000
	stretchedPWLen          = 32
	hkdfLen                 = 32
	errnoInvalidParameter   = 107
	errnoIncorrectEmailCase = 120
	errnoRequestBlocked     = 125
	errnoUnverifiedSession  = 138
)

var (
	ErrReauthRequired = errors.New("требуется повторный вход в аккаунт Mozilla")
	fxaHTTPClient     = func() *http.Client {
		jar, _ := cookiejar.New(nil)
		return &http.Client{
			Timeout: 20 * time.Second,
			Jar:     jar,
		}
	}()
)

func ApplyFirefoxHeaders(req *http.Request) {
	req.Header.Set("User-Agent", FirefoxUserAgent)
	req.Header.Set("Accept", "application/json;q=0.9,*/*;q=0.2")
	req.Header.Set("Accept-Language", "en-US, en")
	req.Header.Set("Accept-Encoding", "identity")
}

type OAuthTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type LoginResponse struct {
	UID                string `json:"uid"`
	SessionToken       string `json:"sessionToken"`
	Verified           bool   `json:"verified"`
	VerificationMethod string `json:"verificationMethod"`
	VerificationReason string `json:"verificationReason"`
	Email              string `json:"email"`
}

type APIError struct {
	Status  int    `json:"status"`
	Errno   int    `json:"errno"`
	Email   string `json:"email"`
	Message string `json:"message"`
}

func (e *APIError) Error() string {
	if e.Message != "" {
		return fmt.Sprintf("FxA HTTP %d (errno %d): %s", e.Status, e.Errno, e.Message)
	}
	return fmt.Sprintf("FxA HTTP %d (errno %d)", e.Status, e.Errno)
}

// DeriveAuthPW stretches email + password using 1000 rounds of PBKDF2-SHA256
// followed by HKDF-SHA256 ("identity.mozilla.com/picl/v1/authPW").
func DeriveAuthPW(email, password string) ([]byte, error) {
	salt := []byte(protocolVersion + "quickStretch:" + email)
	quickStretched := pbkdf2.Key([]byte(password), salt, pbkdf2Rounds, stretchedPWLen, sha256.New)

	hkdfReader := hkdf.New(sha256.New, quickStretched, []byte{0x00}, []byte(protocolVersion+"authPW"))
	authPW := make([]byte, hkdfLen)
	if _, err := io.ReadFull(hkdfReader, authPW); err != nil {
		return nil, err
	}
	return authPW, nil
}

// DeriveHawkCredentials derives a 32-byte HAWK ID and 32-byte MAC key from a 64-hex token.
func DeriveHawkCredentials(tokenHex, contextName string) (id string, key []byte, err error) {
	tokenBytes, err := hex.DecodeString(strings.TrimSpace(tokenHex))
	if err != nil {
		return "", nil, fmt.Errorf("некорректный hex-токен: %w", err)
	}
	info := []byte(protocolVersion + contextName)
	hkdfReader := hkdf.New(sha256.New, tokenBytes, nil, info)
	out := make([]byte, 96)
	if _, err := io.ReadFull(hkdfReader, out); err != nil {
		return "", nil, err
	}
	return hex.EncodeToString(out[:32]), out[32:64], nil
}

// HawkHeader signs an HTTP request with HAWK HMAC-SHA256.
func HawkHeader(method, rawURL, hawkID string, hawkKey []byte, payload string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, 6)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	nonceStr := hex.EncodeToString(nonce)
	ts := fmt.Sprintf("%d", time.Now().Unix())

	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}

	var payloadHash string
	if payload != "" {
		h := sha256.New()
		h.Write([]byte("hawk.1.payload\napplication/json\n"))
		h.Write([]byte(payload))
		h.Write([]byte("\n"))
		payloadHash = hex.EncodeToString(h.Sum(nil))
	}

	normalized := strings.Join([]string{
		"hawk.1.header",
		ts,
		nonceStr,
		strings.ToUpper(method),
		u.RequestURI(),
		u.Hostname(),
		port,
		payloadHash,
		"",
		"",
	}, "\n")

	mac := hmac.New(sha256.New, hawkKey)
	mac.Write([]byte(normalized))
	macStr := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf(`Hawk id="%s", ts="%s", nonce="%s", mac="%s"`, hawkID, ts, nonceStr, macStr)
	if payloadHash != "" {
		header += fmt.Sprintf(`, hash="%s"`, payloadHash)
	}
	return header, nil
}

// LoginWithPassword signs in to FxA using the exact desktop Firefox payload.
// Automatically retries if FxA reports errno 120 (email case mismatch) or errno 107.
func LoginWithPassword(ctx context.Context, email, password, unblockCode string) (*LoginResponse, error) {
	email = strings.TrimSpace(email)
	resp, err := loginAs(ctx, email, "", password, unblockCode)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Errno == errnoIncorrectEmailCase && apiErr.Email != "" && apiErr.Email != email {
		return loginAs(ctx, apiErr.Email, email, password, unblockCode)
	}
	return resp, err
}

func loginAs(ctx context.Context, stretchEmail, originalEmail, password, unblockCode string) (*LoginResponse, error) {
	resp, err := loginAttempt(ctx, stretchEmail, originalEmail, password, "email-2fa", unblockCode)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Errno == errnoInvalidParameter {
		resp, err = loginAttempt(ctx, stretchEmail, originalEmail, password, "", unblockCode)
	}
	if err != nil {
		return nil, err
	}
	resp.Email = stretchEmail
	return resp, nil
}

func loginAttempt(ctx context.Context, stretchEmail, originalEmail, password, verificationMethod, unblockCode string) (*LoginResponse, error) {
	authPW, err := DeriveAuthPW(stretchEmail, password)
	if err != nil {
		return nil, err
	}
	bodyMap := map[string]string{
		"email":  stretchEmail,
		"authPW": hex.EncodeToString(authPW),
	}
	if originalEmail != "" {
		bodyMap["originalLoginEmail"] = originalEmail
	}
	if verificationMethod != "" {
		bodyMap["verificationMethod"] = verificationMethod
	}
	if strings.TrimSpace(unblockCode) != "" {
		bodyMap["unblockCode"] = strings.ToUpper(strings.TrimSpace(unblockCode))
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AuthServer+"/account/login", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	ApplyFirefoxHeaders(req)

	resp, err := fxaHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, apiErr)
		return nil, apiErr
	}

	var loginResp LoginResponse
	if err := json.Unmarshal(data, &loginResp); err != nil || loginResp.SessionToken == "" {
		return nil, errors.New("пустой sessionToken в ответе FxA")
	}
	return &loginResp, nil
}

// SendUnblockCode requests FxA to email an 8-character unblock code when errno 125 occurs.
func SendUnblockCode(ctx context.Context, email string) error {
	bodyBytes, _ := json.Marshal(map[string]string{"email": strings.TrimSpace(email)})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AuthServer+"/account/login/send_unblock_code", bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	ApplyFirefoxHeaders(req)

	resp, err := fxaHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("send_unblock_code HTTP %d", resp.StatusCode)
	}
	return nil
}

// VerifySessionCode confirms an unverified sessionToken using the 6-digit code
// sent to the account's email (POST /v1/session/verify_code).
func VerifySessionCode(ctx context.Context, sessionTokenHex, code string) error {
	hawkID, hawkKey, err := DeriveHawkCredentials(sessionTokenHex, "sessionToken")
	if err != nil {
		return err
	}
	cleanCode := strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return r
		}
		return -1
	}, code)

	bodyBytes, _ := json.Marshal(map[string]string{"code": cleanCode})
	verifyURL := AuthServer + "/session/verify_code"
	authHeader, err := HawkHeader(http.MethodPost, verifyURL, hawkID, hawkKey, string(bodyBytes))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader)
	ApplyFirefoxHeaders(req)

	resp, err := fxaHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
		apiErr := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, apiErr)
		return apiErr
	}
	return nil
}

// VerifySessionTOTP confirms an unverified sessionToken using a 6-digit TOTP authenticator code.
func VerifySessionTOTP(ctx context.Context, sessionTokenHex, code string) error {
	hawkID, hawkKey, err := DeriveHawkCredentials(sessionTokenHex, "sessionToken")
	if err != nil {
		return err
	}
	cleanCode := strings.TrimSpace(code)
	bodyBytes, _ := json.Marshal(map[string]string{"code": cleanCode})
	verifyURL := AuthServer + "/session/verify/totp"
	authHeader, err := HawkHeader(http.MethodPost, verifyURL, hawkID, hawkKey, string(bodyBytes))
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, verifyURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader)
	ApplyFirefoxHeaders(req)

	resp, err := fxaHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, apiErr)
		return apiErr
	}
	var res struct {
		Success bool `json:"success"`
	}
	if err := json.Unmarshal(data, &res); err != nil || !res.Success {
		return errors.New("неверный 2FA TOTP код")
	}
	return nil
}

// SessionVerified checks GET /v1/recovery_email/status to see if a browser sessionToken is verified.
func SessionVerified(ctx context.Context, sessionTokenHex string) (bool, error) {
	hawkID, hawkKey, err := DeriveHawkCredentials(sessionTokenHex, "sessionToken")
	if err != nil {
		return false, err
	}
	statusURL := AuthServer + "/recovery_email/status"
	authHeader, err := HawkHeader(http.MethodGet, statusURL, hawkID, hawkKey, "")
	if err != nil {
		return false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, statusURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", authHeader)
	ApplyFirefoxHeaders(req)

	resp, err := fxaHTTPClient.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("recovery_email/status HTTP %d", resp.StatusCode)
	}
	var st struct {
		Verified bool `json:"verified"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		return false, err
	}
	return st.Verified, nil
}

// ExchangeSessionForOAuth exchanges a 64-hex FxA sessionToken for OAuth access & refresh tokens.
// Tries direct fxa-credentials grant first, then falls back to PKCE S256 authorization code.
func ExchangeSessionForOAuth(ctx context.Context, sessionTokenHex string) (*OAuthTokens, error) {
	hawkID, hawkKey, err := DeriveHawkCredentials(sessionTokenHex, "sessionToken")
	if err != nil {
		return nil, err
	}

	// 1. Try direct grant_type = "fxa-credentials" on /v1/oauth/token
	directURL := AuthServer + "/oauth/token"
	directBody, _ := json.Marshal(map[string]string{
		"client_id":   FirefoxClientID,
		"grant_type":  "fxa-credentials",
		"scope":       OAuthScope,
		"access_type": "offline",
	})
	if directHeader, err := HawkHeader(http.MethodPost, directURL, hawkID, hawkKey, string(directBody)); err == nil {
		if req, err := http.NewRequestWithContext(ctx, http.MethodPost, directURL, bytes.NewReader(directBody)); err == nil {
			req.Header.Set("Content-Type", "application/json; charset=utf-8")
			req.Header.Set("Authorization", directHeader)
			ApplyFirefoxHeaders(req)
			if resp, err := fxaHTTPClient.Do(req); err == nil {
				data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
				resp.Body.Close()
				if resp.StatusCode == http.StatusOK {
					var out OAuthTokens
					if json.Unmarshal(data, &out) == nil && out.AccessToken != "" {
						out.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
						return &out, nil
					}
				}
			}
		}
	}

	// 2. Fallback: PKCE code verifier + S256 challenge via /v1/oauth/authorization
	var verifierBytes [32]byte
	if _, err := rand.Read(verifierBytes[:]); err != nil {
		return nil, err
	}
	verifier := base64.RawURLEncoding.EncodeToString(verifierBytes[:])
	challengeSum := sha256.Sum256([]byte(verifier))
	stateBytes := make([]byte, 12)
	_, _ = rand.Read(stateBytes)
	state := hex.EncodeToString(stateBytes)

	authURL := AuthServer + "/oauth/authorization"
	authBody, _ := json.Marshal(map[string]string{
		"client_id":             FirefoxClientID,
		"response_type":         "code",
		"scope":                 OAuthScope,
		"state":                 state,
		"access_type":           "offline",
		"code_challenge":        base64.RawURLEncoding.EncodeToString(challengeSum[:]),
		"code_challenge_method": "S256",
	})

	authHeader, err := HawkHeader(http.MethodPost, authURL, hawkID, hawkKey, string(authBody))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, authURL, bytes.NewReader(authBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Set("Authorization", authHeader)
	ApplyFirefoxHeaders(req)

	resp, err := fxaHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if resp.StatusCode != http.StatusOK {
		apiErr := &APIError{Status: resp.StatusCode}
		_ = json.Unmarshal(data, apiErr)
		return nil, apiErr
	}

	var codeResp struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(data, &codeResp); err != nil || codeResp.Code == "" {
		return nil, errors.New("не получен код авторизации OAuth")
	}

	tokenBody, _ := json.Marshal(map[string]string{
		"client_id":     FirefoxClientID,
		"grant_type":    "authorization_code",
		"code":          codeResp.Code,
		"code_verifier": verifier,
	})
	tokReq, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthTokenURL, bytes.NewReader(tokenBody))
	if err != nil {
		return nil, err
	}
	tokReq.Header.Set("Content-Type", "application/json; charset=utf-8")
	ApplyFirefoxHeaders(tokReq)

	tokResp, err := fxaHTTPClient.Do(tokReq)
	if err != nil {
		return nil, err
	}
	defer tokResp.Body.Close()

	if tokResp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth token exchange HTTP %d", tokResp.StatusCode)
	}

	var out OAuthTokens
	if err := json.NewDecoder(tokResp.Body).Decode(&out); err != nil {
		return nil, err
	}
	out.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return &out, nil
}

// RefreshAccessToken refreshes an expired OAuth access_token using the stored refresh_token.
func RefreshAccessToken(ctx context.Context, refreshToken string) (*OAuthTokens, error) {
	body, _ := json.Marshal(map[string]string{
		"client_id":     FirefoxClientID,
		"grant_type":    "refresh_token",
		"refresh_token": refreshToken,
		"scope":         OAuthScope,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, OAuthTokenURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	ApplyFirefoxHeaders(req)

	resp, err := fxaHTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusBadRequest || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return nil, ErrReauthRequired
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth refresh HTTP %d", resp.StatusCode)
	}

	var out OAuthTokens
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.RefreshToken == "" {
		out.RefreshToken = refreshToken
	}
	out.ExpiresAt = time.Now().Add(time.Duration(out.ExpiresIn) * time.Second)
	return &out, nil
}
