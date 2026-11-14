// Package fxa implements Mozilla Firefox Accounts authentication:
// PBKDF2 + HKDF password stretching, HAWK request signing, session login,
// PKCE OAuth code exchange, and access token refresh.
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
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/hkdf"
	"golang.org/x/crypto/pbkdf2"
)

const (
	AuthServer       = "https://api.accounts.firefox.com/v1"
	OAuthTokenURL    = "https://oauth.accounts.firefox.com/v1/token"
	FirefoxClientID  = "5882386c6d801776"
	OAuthScope       = "profile https://identity.mozilla.com/apps/vpn"
	FirefoxUserAgent = "Mozilla/5.0 (Android 14; Mobile; rv:145.0) Gecko/145.0 Firefox/145.0"

	protocolVersion          = "identity.mozilla.com/picl/v1/"
	pbkdf2Rounds             = 1000
	stretchedPWLen           = 32
	hkdfLen                  = 32
	errnoIncorrectEmailCase  = 120
)

var ErrReauthRequired = errors.New("требуется повторный вход в аккаунт Mozilla")

type OAuthTokens struct {
	AccessToken  string    `json:"access_token"`
	RefreshToken string    `json:"refresh_token"`
	ExpiresIn    int       `json:"expires_in"`
	ExpiresAt    time.Time `json:"expires_at"`
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
		return "", nil, fmt.Errorf("invalid token hex: %w", err)
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

// LoginWithPassword signs in to FxA and returns a sessionToken (64-hex).
// Automatically retries once if FxA reports errno 120 (canonical email case mismatch).
func LoginWithPassword(ctx context.Context, email, password string) (sessionToken string, canonicalEmail string, err error) {
	email = strings.TrimSpace(strings.ToLower(email))
	tok, corrEmail, err := loginAttempt(ctx, email, "", password)
	if err != nil && corrEmail != "" && corrEmail != email {
		return loginAttempt(ctx, corrEmail, email, password)
	}
	return tok, email, err
}

func loginAttempt(ctx context.Context, stretchEmail, originalEmail, password string) (string, string, error) {
	authPW, err := DeriveAuthPW(stretchEmail, password)
	if err != nil {
		return "", "", err
	}
	bodyMap := map[string]any{
		"email":              stretchEmail,
		"authPW":             hex.EncodeToString(authPW),
		"reason":             "login",
		"service":            FirefoxClientID,
		"verificationMethod": "email-2fa",
	}
	if originalEmail != "" {
		bodyMap["originalLoginEmail"] = originalEmail
	}
	bodyBytes, _ := json.Marshal(bodyMap)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, AuthServer+"/account/login?keys=true", bytes.NewReader(bodyBytes))
	if err != nil {
		return "", "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", FirefoxUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(io.LimitReader(resp.Body, 32*1024))
	if resp.StatusCode != http.StatusOK {
		var apiErr struct {
			Errno   int    `json:"errno"`
			Email   string `json:"email"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(data, &apiErr)
		if apiErr.Errno == errnoIncorrectEmailCase && apiErr.Email != "" {
			return "", apiErr.Email, fmt.Errorf("email case mismatch")
		}
		return "", "", fmt.Errorf("FxA login HTTP %d (errno %d): %s", resp.StatusCode, apiErr.Errno, apiErr.Message)
	}

	var loginResp struct {
		SessionToken string `json:"sessionToken"`
	}
	if err := json.Unmarshal(data, &loginResp); err != nil || loginResp.SessionToken == "" {
		return "", "", errors.New("пустой sessionToken в ответе FxA")
	}
	return loginResp.SessionToken, stretchEmail, nil
}

// ExchangeSessionForOAuth exchanges a 64-hex FxA sessionToken for OAuth access & refresh tokens
// using HAWK authentication and PKCE fallback.
func ExchangeSessionForOAuth(ctx context.Context, sessionTokenHex string) (*OAuthTokens, error) {
	hawkID, hawkKey, err := DeriveHawkCredentials(sessionTokenHex, "sessionToken")
	if err != nil {
		return nil, err
	}

	// PKCE code verifier + S256 challenge
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader)
	req.Header.Set("User-Agent", FirefoxUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OAuth authorization HTTP %d", resp.StatusCode)
	}

	var codeResp struct {
		Code string `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&codeResp); err != nil || codeResp.Code == "" {
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
	tokReq.Header.Set("Content-Type", "application/json")
	tokReq.Header.Set("User-Agent", FirefoxUserAgent)

	tokResp, err := http.DefaultClient.Do(tokReq)
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
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", FirefoxUserAgent)

	resp, err := http.DefaultClient.Do(req)
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
