export interface AndroidProjectFile {
  path: string;
  language: 'kotlin' | 'go' | 'xml' | 'gradle' | 'markdown';
  title: string;
  description: string;
  content: string;
}

export interface ProtocolStage {
  step: string;
  title: string;
  endpoint: string;
  method: string;
  summary: string;
  details: string[];
  codeSnippet: string;
}

export const MOZILLA_VPN_PROTOCOL_STAGES: ProtocolStage[] = [
  {
    step: '01',
    title: 'Растяжение ключа пароля (FxA Key Stretching)',
    endpoint: 'identity.mozilla.com/picl/v1/quickStretch',
    method: 'PBKDF2 + HKDF-SHA256',
    summary: 'Клиент никогда не отправляет пароль в открытом виде. Email и пароль проходят через 1000 раундов PBKDF2-HMAC-SHA256 и HKDF-SHA256 для получения 32-байтного authPW.',
    details: [
      'Salt для PBKDF2: "identity.mozilla.com/picl/v1/quickStretch:" + email (регистр email критичен, при errno 120 сервер возвращает точный регистр аккаунта)',
      'Результат quickStretch (32 байта) подаётся в HKDF-SHA256 с info="identity.mozilla.com/picl/v1/authPW" и salt=0x00',
      'Реализовано в переписанном модуле core/fxa/fxa.go (DeriveAuthPW + LoginWithPassword)'
    ],
    codeSnippet: `salt := []byte("identity.mozilla.com/picl/v1/quickStretch:" + email)
quickStretchedPW := pbkdf2.Key([]byte(password), salt, 1000, 32, sha256.New)
hkdfReader := hkdf.New(sha256.New, quickStretchedPW, []byte{0x00}, []byte("identity.mozilla.com/picl/v1/authPW"))`
  },
  {
    step: '02',
    title: 'Аутентификация Firefox Accounts (HAWK + PKCE S256)',
    endpoint: 'https://api.accounts.firefox.com/v1/oauth/authorization',
    method: 'HAWK HMAC-SHA256',
    summary: 'Получение 64-символьного hex sessionToken через прямой вход API либо WebChannel и выпуск OAuth-токенов с подписью HAWK.',
    details: [
      'Прямой API: POST /v1/account/login?keys=true с verificationMethod="email-2fa" и User-Agent Firefox 145',
      'Из sessionToken через HKDF-SHA256 (info="identity.mozilla.com/picl/v1/sessionToken") выводятся hawkID (32B) и hawkKey (32B) для подписи HAWK',
      'Реализовано в core/fxa/fxa.go (DeriveHawkCredentials, HawkHeader, ExchangeSessionForOAuth, RefreshAccessToken)'
    ],
    codeSnippet: `// HAWK MAC: HMAC-SHA256(hawkKey, "hawk.1.header\\n" + ts + "\\n" + nonce + "\\nPOST\\n/v1/oauth/authorization...")
const FirefoxClientID = "5882386c6d801776"
const OAuthScope      = "profile https://identity.mozilla.com/apps/vpn"`
  },
  {
    step: '03',
    title: 'Guardian ProxyPass JWT и авто-ротация 5 ГБ квоты',
    endpoint: 'https://vpn.mozilla.org/api/v1/fpn/token',
    method: 'GET Bearer + X-Quota-*',
    summary: 'Активация подписки FPN (/api/v1/fpn/activate), выпуск короткоживущего JWT-пропуска ProxyPass и автоматическое переключение аккаунтов.',
    details: [
      'При HTTP 403 от Guardian модуль core/guardian/guardian.go автоматически вызывает POST https://vpn.mozilla.org/api/v1/fpn/activate',
      'Ответ GET /api/v1/fpn/token содержит JWT токен и заголовки X-Quota-Limit (5 ГБ / месяц), X-Quota-Remaining и X-Quota-Reset',
      'Модуль core/accounts/pool.go отслеживает порог (по умолчанию 95%) и при исчерпании квоты бесшовно переключает туннель на следующий аккаунт через engine.UpdateToken()'
    ],
    codeSnippet: `GET https://vpn.mozilla.org/api/v1/fpn/token
Authorization: Bearer <fxa_oauth_access_token>

// Response Headers:
X-Quota-Limit: 5368709120
X-Quota-Remaining: 4120954880
X-Quota-Reset: 2026-12-01T00:00:00Z`
  },
  {
    step: '04',
    title: 'Remote Settings и мультиплексированный HTTP/2 CONNECT туннель',
    endpoint: 'https://firefox.settings.services.mozilla.com/v1/buckets/main/collections/vpn-serverlist/records',
    method: 'TLS 1.3 ALPN h2 CONNECT',
    summary: 'Загрузка серверов (*.m1.fastly-masque.net:2499), снятие блокировки locked-стран и локальный SOCKS5-сервер для Android VpnService (Tun2Socks).',
    details: [
      'Модуль core/serverlist/serverlist.go загружает все 33+ страны из Remote Settings и снимает ограничение locked: true',
      'Модуль core/tunnel/tunnel.go поднимает локальный SOCKS5-сервер для Tun2Socks и мультиплексирует потоки внутри одного TLS 1.3 HTTP/2 соединения с заголовком Proxy-Authorization: Bearer <ProxyPass_JWT>',
      'Замер реального пинга (ProbeLatency) измеряет время установки CONNECT-потока до www.google.com:443 через выходной узел выбранной страны'
    ],
    codeSnippet: `CONNECT target.example.com:443 HTTP/2
Host: target.example.com:443
Proxy-Authorization: Bearer <Guardian_ProxyPass_JWT>
User-Agent: Mozilla/5.0 (Android 14; Mobile; rv:145.0) Gecko/145.0 Firefox/145.0`
  }
];

export const ANDROID_PROJECT_FILES: AndroidProjectFile[] = [
  {
    path: 'core/mobile/mobile.go',
    language: 'go',
    title: 'core/mobile/mobile.go (Точка входа gomobile bind -> core.aar)',
    description: 'Полностью переписанная точка входа Go-ядра для Android без единой лишней зависимости: только управление пулом аккаунтов, выбор локации, замер пинга и запуск туннеля.',
    content: `// Package mobile is the gomobile bind entry point for Android (libs/core.aar).
// Contains ONLY Mozilla account management, automatic 5 GB quota rotation,
// Remote Settings server selection, and the local SOCKS5 -> H2 CONNECT tunnel.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"time"

	"mozillavpnlite/core/accounts"
	"mozillavpnlite/core/fxa"
	"mozillavpnlite/core/serverlist"
	"mozillavpnlite/core/tunnel"
)

var (
	mu           sync.Mutex
	pool         *accounts.Pool
	engine       = tunnel.NewEngine()
	locations    []serverlist.Location
	selectedCode = "DE"
	connected    = false
)

// Start initializes the account pool and server list in dataDir.
func Start(dataDir string, autoConnect bool) error {
	mu.Lock()
	p, err := accounts.LoadOrCreatePool(dataDir)
	if err != nil {
		mu.Unlock()
		return err
	}
	pool = p
	mu.Unlock()

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if list, err := serverlist.Fetch(ctx); err == nil && len(list) > 0 {
			mu.Lock()
			locations = list
			mu.Unlock()
		}
	}()

	if autoConnect {
		return Connect()
	}
	return nil
}

// Connect obtains a fresh Guardian ProxyPass from the active Mozilla account
// (rotating automatically if its 5 GB quota is exhausted) and starts the local tunnel.
func Connect() error {
	mu.Lock()
	p := pool
	loc := currentLocationLocked()
	mu.Unlock()

	if p == nil {
		return errors.New("ядро не инициализировано")
	}
	active := p.ActiveAccount()
	if active == nil {
		return errors.New("добавьте хотя бы один аккаунт Mozilla")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pass, err := p.EnsureFreshPass(ctx, active)
	if err != nil {
		return err
	}

	_, err = engine.Start("127.0.0.1:0", loc.Endpoint(), pass.RawToken)
	if err != nil {
		return err
	}

	mu.Lock()
	connected = true
	mu.Unlock()
	return nil
}

// Disconnect stops the local SOCKS5 tunnel.
func Disconnect() {
	engine.Stop()
	mu.Lock()
	connected = false
	mu.Unlock()
}

// SwitchAccount immediately switches the active Mozilla account and hot-swaps
// its ProxyPass JWT in the running tunnel without dropping the VPN interface.
func SwitchAccount(accountID string) error {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p == nil {
		return errors.New("ядро не инициализировано")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pass, err := p.Activate(ctx, accountID)
	if err != nil {
		return err
	}
	engine.UpdateToken(pass.RawToken)
	return nil
}

// AddAccountPassword signs in with email + password (PBKDF2 + HKDF + HAWK) and adds the account.
func AddAccountPassword(email, password string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	sessionToken, canonicalEmail, err := fxa.LoginWithPassword(ctx, email, password)
	if err != nil {
		return err
	}
	return AddAccountSessionToken(canonicalEmail, sessionToken)
}

// AddAccountSessionToken adds an account using a 64-hex FxA sessionToken from WebChannel.
func AddAccountSessionToken(email, sessionTokenHex string) error {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p == nil {
		return errors.New("ядро не инициализировано")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	_, err := p.AddWithSessionToken(ctx, email, sessionTokenHex)
	return err
}

// SetLocation changes the active server country code and switches the upstream Fastly node.
func SetLocation(countryCode string) {
	mu.Lock()
	selectedCode = strings.ToUpper(strings.TrimSpace(countryCode))
	loc := currentLocationLocked()
	mu.Unlock()

	engine.SwitchServer(loc.Endpoint())
}

// PingAllLocations measures real HTTP/2 CONNECT latency across all Mozilla locations.
func PingAllLocations() error {
	mu.Lock()
	p := pool
	list := append([]serverlist.Location(nil), locations...)
	mu.Unlock()

	if p == nil {
		return errors.New("ядро не инициализировано")
	}
	active := p.ActiveAccount()
	if active == nil {
		return errors.New("для замера пинга нужен аккаунт Mozilla")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	pass, err := p.EnsureFreshPass(ctx, active)
	if err != nil {
		return err
	}

	measured := serverlist.MeasureLatencies(ctx, list, func(c context.Context, endpoint string) (time.Duration, error) {
		return tunnel.ProbeLatency(c, endpoint, pass.RawToken)
	})

	mu.Lock()
	locations = measured
	mu.Unlock()
	return nil
}

func SocksPort() int {
	return engine.Port()
}

// StatusJSON returns the complete snapshot for the Android Jetpack Compose UI.
func StatusJSON() string {
	mu.Lock()
	defer mu.Unlock()

	var accs []*accounts.Account
	threshold := 95
	if pool != nil {
		accs = pool.Accounts
		threshold = pool.ThresholdPercent
	}

	payload := map[string]any{
		"connected":         connected,
		"socks_port":        engine.Port(),
		"selected_location": selectedCode,
		"location":          currentLocationLocked(),
		"threshold_percent": threshold,
		"accounts":          accs,
		"locations":         locations,
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func currentLocationLocked() serverlist.Location {
	for _, l := range locations {
		if strings.EqualFold(l.Code, selectedCode) {
			return l
		}
	}
	if len(locations) > 0 {
		return locations[0]
	}
	return serverlist.Location{
		Code:       "DE",
		Name:       "Germany",
		City:       "Frankfurt (FRA)",
		ServerHost: "fra104.m1.fastly-masque.net",
		ServerPort: 2499,
	}
}
`
  },
  {
    path: 'core/fxa/fxa.go',
    language: 'go',
    title: 'core/fxa/fxa.go (Криптография Firefox Accounts: PBKDF2 + HKDF + HAWK + PKCE)',
    description: 'Чистая реализация протокола авторизации Mozilla FxA: растяжение пароля, подпись HAWK HMAC-SHA256, обработка errno 120, обмен sessionToken на OAuth-токены и автообновление access_token.',
    content: `// Package fxa implements Mozilla Firefox Accounts authentication:
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
	AccessToken  string    \`json:"access_token"\`
	RefreshToken string    \`json:"refresh_token"\`
	ExpiresIn    int       \`json:"expires_in"\`
	ExpiresAt    time.Time \`json:"expires_at"\`
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
		h.Write([]byte("hawk.1.payload\\napplication/json\\n"))
		h.Write([]byte(payload))
		h.Write([]byte("\\n"))
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
	}, "\\n")

	mac := hmac.New(sha256.New, hawkKey)
	mac.Write([]byte(normalized))
	macStr := hex.EncodeToString(mac.Sum(nil))

	header := fmt.Sprintf(\`Hawk id="%s", ts="%s", nonce="%s", mac="%s"\`, hawkID, ts, nonceStr, macStr)
	if payloadHash != "" {
		header += fmt.Sprintf(\`, hash="%s"\`, payloadHash)
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
			Errno   int    \`json:"errno"\`
			Email   string \`json:"email"\`
			Message string \`json:"message"\`
		}
		_ = json.Unmarshal(data, &apiErr)
		if apiErr.Errno == errnoIncorrectEmailCase && apiErr.Email != "" {
			return "", apiErr.Email, fmt.Errorf("email case mismatch")
		}
		return "", "", fmt.Errorf("FxA login HTTP %d (errno %d): %s", resp.StatusCode, apiErr.Errno, apiErr.Message)
	}

	var loginResp struct {
		SessionToken string \`json:"sessionToken"\`
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
		Code string \`json:"code"\`
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
`
  },
  {
    path: 'core/guardian/guardian.go',
    language: 'go',
    title: 'core/guardian/guardian.go (Клиент Guardian API и парсер X-Quota-*)',
    description: 'Получение JWT-пропуска ProxyPass от https://vpn.mozilla.org/api/v1/fpn/token, автоматическая бесплатная активация при HTTP 403 и разбор заголовков квоты 5 ГБ.',
    content: `// Package guardian issues ProxyPass bearer JWTs from Mozilla's Guardian API
// (https://vpn.mozilla.org), activates free FPN entitlements on HTTP 403,
// and parses X-Quota-* monthly traffic headers.
package guardian

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"mozillavpnlite/core/fxa"
)

const Endpoint = "https://vpn.mozilla.org"

var (
	ErrTokenInvalid  = errors.New("OAuth токен отклонён Guardian (HTTP 401)")
	ErrNotEntitled   = errors.New("аккаунт ещё не активирован в Guardian (HTTP 403)")
	ErrQuotaExceeded = errors.New("месячная квота 5 ГБ исчерпана (HTTP 429)")
)

type QuotaUsage struct {
	Unlimited      bool      \`json:"unlimited"\`
	LimitBytes     int64     \`json:"limit_bytes"\`
	RemainingBytes int64     \`json:"remaining_bytes"\`
	ResetAt        time.Time \`json:"reset_at"\`
}

func (u *QuotaUsage) UsedBytes() int64 {
	if u == nil || u.Unlimited || u.LimitBytes <= u.RemainingBytes {
		return 0
	}
	return u.LimitBytes - u.RemainingBytes
}

func (u *QuotaUsage) UsedPercent() float64 {
	if u == nil || u.Unlimited || u.LimitBytes <= 0 {
		return 0
	}
	return float64(u.UsedBytes()) * 100.0 / float64(u.LimitBytes)
}

type ProxyPass struct {
	RawToken  string      \`json:"raw_token"\`
	ExpiresAt time.Time   \`json:"expires_at"\`
	Usage     *QuotaUsage \`json:"usage,omitempty"\`
}

// FetchProxyPass requests a short-lived ProxyPass JWT from /api/v1/fpn/token.
// If Guardian responds with HTTP 403, it automatically calls /api/v1/fpn/activate and retries.
func FetchProxyPass(ctx context.Context, accessToken string) (*ProxyPass, error) {
	pass, err := fetchTokenOnce(ctx, accessToken)
	if errors.Is(err, ErrNotEntitled) {
		if actErr := Activate(ctx, accessToken); actErr == nil {
			return fetchTokenOnce(ctx, accessToken)
		}
	}
	return pass, err
}

func fetchTokenOnce(ctx context.Context, accessToken string) (*ProxyPass, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint+"/api/v1/fpn/token", nil)
	if err != nil {
		return nil, err
	}
	applyHeaders(req, accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	usage := parseQuotaHeaders(resp.Header)

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, ErrTokenInvalid
	case http.StatusForbidden:
		return nil, ErrNotEntitled
	case http.StatusTooManyRequests:
		return &ProxyPass{Usage: usage}, ErrQuotaExceeded
	default:
		return nil, fmt.Errorf("Guardian HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return nil, err
	}
	var tokenResp struct {
		Token string \`json:"token"\`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.Token == "" {
		return nil, errors.New("пустой токен ProxyPass в ответе Guardian")
	}

	exp := parseJWTExpiry(tokenResp.Token)
	return &ProxyPass{
		RawToken:  tokenResp.Token,
		ExpiresAt: exp,
		Usage:     usage,
	}, nil
}

// Activate enables the Firefox Private Network entitlement on the Mozilla account.
func Activate(ctx context.Context, accessToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint+"/api/v1/fpn/activate", nil)
	if err != nil {
		return err
	}
	applyHeaders(req, accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Guardian activate HTTP %d", resp.StatusCode)
	}
	return nil
}

func applyHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)
}

func parseQuotaHeaders(h http.Header) *QuotaUsage {
	if strings.EqualFold(strings.TrimSpace(h.Get("X-Quota-Unlimited")), "true") {
		return &QuotaUsage{Unlimited: true}
	}
	limit, err1 := strconv.ParseInt(strings.TrimSpace(h.Get("X-Quota-Limit")), 10, 64)
	rem, err2 := strconv.ParseInt(strings.TrimSpace(h.Get("X-Quota-Remaining")), 10, 64)
	if err1 != nil || err2 != nil || limit <= 0 {
		return nil
	}
	var resetAt time.Time
	rawReset := strings.TrimSpace(h.Get("X-Quota-Reset"))
	if t, err := time.Parse(time.RFC3339Nano, rawReset); err == nil {
		resetAt = t
	} else if sec, err := strconv.ParseInt(rawReset, 10, 64); err == nil && sec > 0 {
		resetAt = time.Unix(sec, 0)
	}
	return &QuotaUsage{
		LimitBytes:     limit,
		RemainingBytes: rem,
		ResetAt:        resetAt,
	}
}

func parseJWTExpiry(rawJWT string) time.Time {
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		return time.Now().Add(6 * time.Hour)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Now().Add(6 * time.Hour)
	}
	var claims struct {
		Exp int64 \`json:"exp"\`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil || claims.Exp <= 0 {
		return time.Now().Add(6 * time.Hour)
	}
	return time.Unix(claims.Exp, 0)
}
`
  },
  {
    path: 'core/accounts/pool.go',
    language: 'go',
    title: 'core/accounts/pool.go (Пул аккаунтов Mozilla и авто-ротация по квоте)',
    description: 'Управление списком аккаунтов Mozilla, мгновенное переключение активного профиля в 1 касание и автоматический переход на следующий аккаунт при достижении порога 5 ГБ.',
    content: `// Package accounts manages the pool of Mozilla accounts, credential
// persistence, instant 1-tap profile switching, and automatic failover when an
// account's 5 GB monthly quota reaches the configured threshold.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mozillavpnlite/core/fxa"
	"mozillavpnlite/core/guardian"
)

type Status string

const (
	StatusReady          Status = "ready"
	StatusReserved       Status = "reserved"
	StatusReauthRequired Status = "reauth_required"
)

type Account struct {
	ID       string               \`json:"id"\`
	Email    string               \`json:"email"\`
	Position int                  \`json:"position"\`
	Active   bool                 \`json:"active"\`
	Enabled  bool                 \`json:"enabled"\`
	Status   Status               \`json:"status"\`
	Reason   string               \`json:"reason,omitempty"\`
	Tokens   fxa.OAuthTokens      \`json:"tokens"\`
	LastPass *guardian.ProxyPass  \`json:"last_pass,omitempty"\`
	Usage    *guardian.QuotaUsage \`json:"usage,omitempty"\`
}

type Pool struct {
	mu               sync.Mutex
	filePath         string
	ThresholdPercent int        \`json:"threshold_percent"\`
	Accounts         []*Account \`json:"accounts"\`
}

func LoadOrCreatePool(dataDir string) (*Pool, error) {
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	fp := filepath.Join(dataDir, "accounts_pool.json")
	p := &Pool{
		filePath:         fp,
		ThresholdPercent: 95,
		Accounts:         []*Account{},
	}
	if data, err := os.ReadFile(fp); err == nil {
		_ = json.Unmarshal(data, p)
	}
	if p.ThresholdPercent < 50 || p.ThresholdPercent > 100 {
		p.ThresholdPercent = 95
	}
	p.recomputeLocked()
	return p, nil
}

func (p *Pool) saveLocked() error {
	p.recomputeLocked()
	data, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p.filePath, data, 0600)
}

func (p *Pool) recomputeLocked() {
	for i, acc := range p.Accounts {
		acc.Position = i + 1
		if acc.Usage != nil && !acc.Usage.Unlimited && acc.Usage.LimitBytes > 0 {
			if acc.Usage.RemainingBytes <= 0 || acc.Usage.UsedPercent() >= float64(p.ThresholdPercent) {
				if len(p.Accounts) > 1 {
					acc.Status = StatusReserved
					acc.Reason = "Достигнут порог месячной квоты 5 ГБ"
				}
			} else if acc.Status == StatusReserved {
				acc.Status = StatusReady
				acc.Reason = ""
			}
		}
	}
}

// AddWithSessionToken exchanges an FxA sessionToken for OAuth tokens, fetches a
// Guardian ProxyPass, and appends the account to the rotation pool.
func (p *Pool) AddWithSessionToken(ctx context.Context, email, sessionTokenHex string) (*Account, error) {
	tokens, err := fxa.ExchangeSessionForOAuth(ctx, sessionTokenHex)
	if err != nil {
		return nil, err
	}
	pass, err := guardian.FetchProxyPass(ctx, tokens.AccessToken)
	if err != nil {
		return nil, err
	}

	var idBytes [6]byte
	_, _ = rand.Read(idBytes[:])

	p.mu.Lock()
	defer p.mu.Unlock()

	acc := &Account{
		ID:       "acc-" + hex.EncodeToString(idBytes[:]),
		Email:    email,
		Active:   len(p.Accounts) == 0,
		Enabled:  true,
		Status:   StatusReady,
		Tokens:   *tokens,
		LastPass: pass,
		Usage:    pass.Usage,
	}
	p.Accounts = append(p.Accounts, acc)
	_ = p.saveLocked()
	return acc, nil
}

// Activate immediately makes the given account ID active and returns a valid ProxyPass.
func (p *Pool) Activate(ctx context.Context, id string) (*guardian.ProxyPass, error) {
	p.mu.Lock()
	var target *Account
	for _, a := range p.Accounts {
		if a.ID == id {
			target = a
			break
		}
	}
	if target == nil {
		p.mu.Unlock()
		return nil, errors.New("аккаунт не найден")
	}
	for _, a := range p.Accounts {
		a.Active = (a.ID == id)
	}
	target.Enabled = true
	_ = p.saveLocked()
	p.mu.Unlock()

	return p.EnsureFreshPass(ctx, target)
}

// EnsureFreshPass returns a valid ProxyPass for acc, refreshing OAuth or rotating
// to the next account in the pool if acc's quota is exhausted.
func (p *Pool) EnsureFreshPass(ctx context.Context, acc *Account) (*guardian.ProxyPass, error) {
	if time.Now().After(acc.Tokens.ExpiresAt.Add(-2 * time.Minute)) {
		refreshed, err := fxa.RefreshAccessToken(ctx, acc.Tokens.RefreshToken)
		if err != nil {
			p.mu.Lock()
			if errors.Is(err, fxa.ErrReauthRequired) {
				acc.Status = StatusReauthRequired
			}
			_ = p.saveLocked()
			p.mu.Unlock()
			return nil, err
		}
		p.mu.Lock()
		acc.Tokens = *refreshed
		_ = p.saveLocked()
		p.mu.Unlock()
	}

	pass, err := guardian.FetchProxyPass(ctx, acc.Tokens.AccessToken)
	p.mu.Lock()
	if pass != nil && pass.Usage != nil {
		acc.Usage = pass.Usage
		acc.LastPass = pass
	}
	_ = p.saveLocked()
	p.mu.Unlock()

	if errors.Is(err, guardian.ErrQuotaExceeded) || (pass != nil && pass.Usage != nil && pass.Usage.UsedPercent() >= float64(p.ThresholdPercent)) {
		return p.RotateToNextReady(ctx, acc.ID)
	}
	return pass, err
}

// RotateToNextReady finds the next enabled account with remaining quota and activates it.
func (p *Pool) RotateToNextReady(ctx context.Context, skipID string) (*guardian.ProxyPass, error) {
	p.mu.Lock()
	var candidate *Account
	for _, a := range p.Accounts {
		if a.ID != skipID && a.Enabled && a.Status == StatusReady {
			candidate = a
			break
		}
	}
	if candidate == nil {
		p.mu.Unlock()
		return nil, errors.New("нет доступных резервных аккаунтов со свободной квотой")
	}
	for _, a := range p.Accounts {
		a.Active = (a.ID == candidate.ID)
	}
	_ = p.saveLocked()
	p.mu.Unlock()

	return p.EnsureFreshPass(ctx, candidate)
}

func (p *Pool) ActiveAccount() *Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.Accounts {
		if a.Active {
			return a
		}
	}
	if len(p.Accounts) > 0 {
		p.Accounts[0].Active = true
		return p.Accounts[0]
	}
	return nil
}
`
  },
  {
    path: 'core/serverlist/serverlist.go',
    language: 'go',
    title: 'core/serverlist/serverlist.go (Загрузка серверов Remote Settings и замер пинга)',
    description: 'Загружает коллекцию vpn-serverlist из Mozilla Remote Settings, открывает доступ ко всем locked-странам и параллельно измеряет задержку до выходных узлов.',
    content: `// Package serverlist fetches Mozilla's Remote Settings vpn-serverlist collection,
// unlocks all locations (ignoring Firefox's locked flag), and measures real
// tunnel latency over HTTP/2 CONNECT.
package serverlist

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"mozillavpnlite/core/fxa"
)

const RemoteSettingsURL = "https://firefox.settings.services.mozilla.com/v1/buckets/main/collections/vpn-serverlist/records"

type Location struct {
	Code       string \`json:"code"\`
	Name       string \`json:"name"\`
	City       string \`json:"city"\`
	ServerHost string \`json:"server_host"\`
	ServerPort int    \`json:"server_port"\`
	Locked     bool   \`json:"locked"\`
	PingMS     int64  \`json:"ping_ms"\`
}

func (l Location) Endpoint() string {
	port := l.ServerPort
	if port <= 0 {
		port = 443
	}
	return fmt.Sprintf("%s:%d", l.ServerHost, port)
}

type rsRecord struct {
	Code   string \`json:"code"\`
	Name   string \`json:"name"\`
	Locked bool   \`json:"locked"\`
	Cities []struct {
		Name    string \`json:"name"\`
		Code    string \`json:"code"\`
		Servers []struct {
			Hostname    string \`json:"hostname"\`
			Port        int    \`json:"port"\`
			Quarantined bool   \`json:"quarantined"\`
		} \`json:"servers"\`
	} \`json:"cities"\`
}

// Fetch retrieves all non-quarantined Mozilla Fastly proxy locations.
func Fetch(ctx context.Context) ([]Location, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, RemoteSettingsURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("serverlist HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 512*1024))
	if err != nil {
		return nil, err
	}

	var envelope struct {
		Data []rsRecord \`json:"data"\`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	var out []Location
	seen := make(map[string]bool)
	for _, rec := range envelope.Data {
		code := strings.ToUpper(strings.TrimSpace(rec.Code))
		if code == "" || code == "REC" || seen[code] {
			continue
		}
		for _, city := range rec.Cities {
			for _, srv := range city.Servers {
				if srv.Quarantined || srv.Hostname == "" {
					continue
				}
				port := srv.Port
				if port <= 0 {
					port = 2499
				}
				seen[code] = true
				out = append(out, Location{
					Code:       code,
					Name:       rec.Name,
					City:       fmt.Sprintf("%s (%s)", city.Name, city.Code),
					ServerHost: srv.Hostname,
					ServerPort: port,
					Locked:     rec.Locked,
				})
				break
			}
			if seen[code] {
				break
			}
		}
	}

	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// MeasureLatencies runs concurrent probes using dialProbe and sorts locations fastest-first.
func MeasureLatencies(ctx context.Context, locations []Location, dialProbe func(ctx context.Context, endpoint string) (time.Duration, error)) []Location {
	results := make([]Location, len(locations))
	copy(results, locations)

	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup

	for i := range results {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()

			d, err := dialProbe(ctx, results[idx].Endpoint())
			if err == nil && d > 0 {
				results[idx].PingMS = d.Milliseconds()
			}
		}(i)
	}
	wg.Wait()

	sort.SliceStable(results, func(i, j int) bool {
		aOk := results[i].PingMS > 0
		bOk := results[j].PingMS > 0
		if aOk != bOk {
			return aOk
		}
		if aOk && results[i].PingMS != results[j].PingMS {
			return results[i].PingMS < results[j].PingMS
		}
		return results[i].Name < results[j].Name
	})
	return results
}
`
  },
  {
    path: 'core/tunnel/tunnel.go',
    language: 'go',
    title: 'core/tunnel/tunnel.go (Локальный SOCKS5 и мультиплексированный HTTP/2 CONNECT)',
    description: 'Принимает потоки от Android Tun2Socks по 127.0.0.1 и туннелирует их через TLS 1.3 HTTP/2 CONNECT на выбранный сервер Fastly с горячей заменой токена ProxyPass.',
    content: `// Package tunnel runs the local loopback SOCKS5 server (for Android Tun2Socks)
// and multiplexes outbound connections over an HTTP/2 TLS 1.3 CONNECT session
// to the selected Mozilla Fastly proxy (*.m1.fastly-masque.net:2499) using the
// active account's Guardian ProxyPass bearer JWT.
package tunnel

import (
	"context"
	"crypto/tls"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"mozillavpnlite/core/fxa"
)

type Engine struct {
	mu          sync.RWMutex
	listener    net.Listener
	proxyHost   string
	bearerToken string
	h2Conn      *http2.ClientConn
	rawTLS      net.Conn
}

func NewEngine() *Engine {
	return &Engine{}
}

// Start begins listening for local SOCKS5 connections from Android VpnService (Tun2Socks).
func (e *Engine) Start(listenAddr, proxyEndpoint, proxyPassJWT string) (int, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.proxyHost = proxyEndpoint
	e.bearerToken = proxyPassJWT

	if e.listener == nil {
		ln, err := net.Listen("tcp", listenAddr)
		if err != nil {
			return 0, err
		}
		e.listener = ln
		go e.acceptLoop(ln)
	}
	return e.listener.Addr().(*net.TCPAddr).Port, nil
}

// UpdateToken hot-swaps the active Mozilla account's ProxyPass JWT without dropping the tunnel.
func (e *Engine) UpdateToken(proxyPassJWT string) {
	e.mu.Lock()
	e.bearerToken = proxyPassJWT
	e.mu.Unlock()
}

// SwitchServer changes the upstream Mozilla Fastly endpoint and resets the pooled H2 connection.
func (e *Engine) SwitchServer(proxyEndpoint string) {
	e.mu.Lock()
	if e.proxyHost != proxyEndpoint {
		e.proxyHost = proxyEndpoint
		if e.rawTLS != nil {
			_ = e.rawTLS.Close()
			e.rawTLS = nil
			e.h2Conn = nil
		}
	}
	e.mu.Unlock()
}

func (e *Engine) Stop() {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.listener != nil {
		_ = e.listener.Close()
		e.listener = nil
	}
	if e.rawTLS != nil {
		_ = e.rawTLS.Close()
		e.rawTLS = nil
		e.h2Conn = nil
	}
}

func (e *Engine) Port() int {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if e.listener == nil {
		return 0
	}
	return e.listener.Addr().(*net.TCPAddr).Port
}

func (e *Engine) acceptLoop(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go e.handleSocks5(conn)
	}
}

func (e *Engine) handleSocks5(client net.Conn) {
	defer client.Close()
	_ = client.SetDeadline(time.Now().Add(15 * time.Second))

	var hdr [2]byte
	if _, err := io.ReadFull(client, hdr[:]); err != nil || hdr[0] != 0x05 {
		return
	}
	methods := make([]byte, int(hdr[1]))
	if _, err := io.ReadFull(client, methods); err != nil {
		return
	}
	_, _ = client.Write([]byte{0x05, 0x00})

	var reqHdr [4]byte
	if _, err := io.ReadFull(client, reqHdr[:]); err != nil || reqHdr[0] != 0x05 || reqHdr[1] != 0x01 {
		return
	}

	var host string
	switch reqHdr[3] {
	case 0x01:
		var ip [4]byte
		if _, err := io.ReadFull(client, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	case 0x03:
		var dlen [1]byte
		if _, err := io.ReadFull(client, dlen[:]); err != nil {
			return
		}
		domain := make([]byte, int(dlen[0]))
		if _, err := io.ReadFull(client, domain); err != nil {
			return
		}
		host = string(domain)
	case 0x04:
		var ip [16]byte
		if _, err := io.ReadFull(client, ip[:]); err != nil {
			return
		}
		host = net.IP(ip[:]).String()
	default:
		return
	}

	var portBuf [2]byte
	if _, err := io.ReadFull(client, portBuf[:]); err != nil {
		return
	}
	port := binary.BigEndian.Uint16(portBuf[:])
	authority := net.JoinHostPort(host, strconv.Itoa(int(port)))

	_, _ = client.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0})
	_ = client.SetDeadline(time.Time{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	streamReader, streamWriter, err := e.openH2ConnectStream(ctx, authority)
	if err != nil {
		return
	}
	defer streamWriter.Close()
	defer streamReader.Close()

	go func() {
		_, _ = io.Copy(streamWriter, client)
		_ = streamWriter.Close()
	}()
	_, _ = io.Copy(client, streamReader)
}

func (e *Engine) ensureH2Session(ctx context.Context) (*http2.ClientConn, string, string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.h2Conn != nil && e.h2Conn.CanTakeNewRequest() {
		return e.h2Conn, e.proxyHost, e.bearerToken, nil
	}

	host, _, err := net.SplitHostPort(e.proxyHost)
	if err != nil {
		return nil, "", "", err
	}

	dialer := &tls.Dialer{
		NetDialer: &net.Dialer{Timeout: 10 * time.Second},
		Config: &tls.Config{
			ServerName: host,
			MinVersion: tls.VersionTLS13,
			NextProtos: []string{"h2"},
		},
	}
	tlsConn, err := dialer.DialContext(ctx, "tcp", e.proxyHost)
	if err != nil {
		return nil, "", "", err
	}

	tr := &http2.Transport{}
	cc, err := tr.NewClientConn(tlsConn)
	if err != nil {
		_ = tlsConn.Close()
		return nil, "", "", err
	}

	e.rawTLS = tlsConn
	e.h2Conn = cc
	return cc, e.proxyHost, e.bearerToken, nil
}

// openH2ConnectStream opens an HTTP/2 CONNECT tunnel stream to targetAuthority.
func (e *Engine) openH2ConnectStream(ctx context.Context, targetAuthority string) (io.ReadCloser, io.WriteCloser, error) {
	cc, proxyHost, token, err := e.ensureH2Session(ctx)
	if err != nil {
		return nil, nil, err
	}

	pr, pw := io.Pipe()
	req, err := http.NewRequestWithContext(ctx, http.MethodConnect, "http://"+targetAuthority, pr)
	if err != nil {
		_ = pw.Close()
		return nil, nil, err
	}
	req.Host = targetAuthority
	req.URL.Host = proxyHost
	req.Header.Set("Proxy-Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)

	resp, err := cc.RoundTrip(req)
	if err != nil {
		_ = pw.Close()
		return nil, nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		_ = resp.Body.Close()
		_ = pw.Close()
		return nil, nil, errors.New(fmt.Sprintf("CONNECT rejected HTTP %d", resp.StatusCode))
	}
	return resp.Body, pw, nil
}

// ProbeLatency measures the round-trip time of an HTTP/2 CONNECT to www.google.com:443
// through the given Mozilla Fastly proxy endpoint.
func ProbeLatency(ctx context.Context, proxyEndpoint, bearerToken string) (time.Duration, error) {
	eng := NewEngine()
	eng.proxyHost = proxyEndpoint
	eng.bearerToken = bearerToken
	defer eng.Stop()

	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	if _, _, _, err := eng.ensureH2Session(probeCtx); err != nil {
		return 0, err
	}

	start := time.Now()
	rc, wc, err := eng.openH2ConnectStream(probeCtx, "www.google.com:443")
	if err != nil {
		return 0, err
	}
	elapsed := time.Since(start)
	_ = wc.Close()
	_ = rc.Close()
	return elapsed, nil
}
`
  },
  {
    path: 'core/go.mod',
    language: 'go',
    title: 'core/go.mod (Зависимости переписанного Go-ядра)',
    description: 'Минимальный go.mod: только стандартная библиотека Go, golang.org/x/crypto (PBKDF2/HKDF) и golang.org/x/net (HTTP/2).',
    content: `module mozillavpnlite/core

go 1.23

require (
	golang.org/x/crypto v0.33.0
	golang.org/x/net v0.35.0
)
`
  },
  {
    path: 'app/src/main/kotlin/app/mozillavpnlite/MainActivity.kt',
    language: 'kotlin',
    title: 'MainActivity.kt (Jetpack Compose Minimal UI)',
    description: 'Единый минималистичный экран Android-приложения: мгновенное переключение аккаунтов Mozilla, выбор локации в 1 касание и запуск VpnService без лишних вкладок.',
    content: `package app.mozillavpnlite

import android.app.Activity
import android.content.Intent
import android.net.VpnService
import android.os.Bundle
import androidx.activity.ComponentActivity
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.text.font.FontWeight
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        MinimalVpnService.startCore(this, on = false)

        setContent {
            MaterialTheme(colorScheme = darkColorScheme(
                primary = Color(0xFF6366F1),
                surface = Color(0xFF0F172A),
                background = Color(0xFF020617)
            )) {
                MinimalVpnApp(activity = this)
            }
        }
    }
}

@Composable
fun MinimalVpnApp(activity: ComponentActivity) {
    val scope = rememberCoroutineScope()
    var tab by remember { mutableIntStateOf(0) } // 0 = Пульт, 1 = Аккаунты, 2 = Серверы
    var state by remember { mutableStateOf(CoreBridge.fetchSnapshot()) }

    val vpnPermissionLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.StartActivityForResult()
    ) { res ->
        if (res.resultCode == Activity.RESULT_OK) {
            MinimalVpnService.turnOn(activity)
            state = CoreBridge.fetchSnapshot()
        }
    }

    LaunchedEffect(Unit) {
        while (true) {
            state = CoreBridge.fetchSnapshot()
            delay(1500)
        }
    }

    Scaffold(
        containerColor = Color(0xFF020617),
        bottomBar = {
            NavigationBar(containerColor = Color(0xFF0F172A)) {
                NavigationBarItem(
                    selected = tab == 0,
                    onClick = { tab = 0 },
                    icon = {},
                    label = { Text("Пульт", fontWeight = FontWeight.SemiBold) }
                )
                NavigationBarItem(
                    selected = tab == 1,
                    onClick = { tab = 1 },
                    icon = {},
                    label = { Text("Аккаунты  (\${state.accounts.size})", fontWeight = FontWeight.SemiBold) }
                )
                NavigationBarItem(
                    selected = tab == 2,
                    onClick = { tab = 2 },
                    icon = {},
                    label = { Text("Серверы  (\${state.locations.size})", fontWeight = FontWeight.SemiBold) }
                )
            }
        }
    ) { padding ->
        Box(modifier = Modifier.padding(padding).fillMaxSize()) {
            when (tab) {
                0 -> QuickSwitchHome(
                    state = state,
                    onToggleVpn = {
                        if (state.connected) {
                            MinimalVpnService.turnOff(activity)
                        } else {
                            val prep: Intent? = VpnService.prepare(activity)
                            if (prep != null) vpnPermissionLauncher.launch(prep)
                            else MinimalVpnService.turnOn(activity)
                        }
                        state = CoreBridge.fetchSnapshot()
                    },
                    onSelectAccount = { id ->
                        scope.launch {
                            mobile.Mobile.switchAccount(id)
                            state = CoreBridge.fetchSnapshot()
                        }
                    },
                    onSelectLocation = { code ->
                        scope.launch {
                            mobile.Mobile.setLocation(code)
                            state = CoreBridge.fetchSnapshot()
                        }
                    }
                )
                1 -> AccountsScreen(
                    state = state,
                    onActivate = { id ->
                        scope.launch {
                            mobile.Mobile.switchAccount(id)
                            state = CoreBridge.fetchSnapshot()
                        }
                    },
                    onAddAccount = { email, pass ->
                        scope.launch {
                            mobile.Mobile.addAccountPassword(email, pass)
                            state = CoreBridge.fetchSnapshot()
                        }
                    }
                )
                2 -> ServersScreen(
                    state = state,
                    onSelect = { code ->
                        scope.launch {
                            mobile.Mobile.setLocation(code)
                            state = CoreBridge.fetchSnapshot()
                            tab = 0
                        }
                    },
                    onPingAll = {
                        scope.launch {
                            mobile.Mobile.pingAllLocations()
                            state = CoreBridge.fetchSnapshot()
                        }
                    }
                )
            }
        }
    }
}
`
  },
  {
    path: 'app/src/main/kotlin/app/mozillavpnlite/MinimalVpnService.kt',
    language: 'kotlin',
    title: 'MinimalVpnService.kt (Android VpnService + Tun2Socks)',
    description: 'Системный сервис VpnService: поднимает TUN-интерфейс (10.0.0.2/32, MTU 1500), исключает сам клиент из маршрутизации и соединяет Tun2Socks с локальным SOCKS5 нашего переписанного Go-ядра.',
    content: `package app.mozillavpnlite

import android.content.Context
import android.content.Intent
import android.net.VpnService
import android.os.Build
import android.os.ParcelFileDescriptor
import java.io.File

class MinimalVpnService : VpnService() {

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == ACTION_STOP) {
            turnOff(this)
            stopSelf()
            return START_NOT_STICKY
        }
        startCore(this, on = true)
        establishTunnel()
        return START_STICKY
    }

    private fun establishTunnel() {
        val socksPort = mobile.Mobile.socksPort()
        val builder = Builder()
            .setSession("Mozilla VPN Lite")
            .setMtu(1500)
            .addAddress("10.0.0.2", 32)
            .addAddress("fd00::2", 128)
            .addRoute("0.0.0.0", 0)
            .addRoute("::", 0)
            .addDnsServer("1.1.1.1")

        // Исключаем сам VPN-клиент, чтобы HTTP/2 CONNECT к Fastly шёл напрямую
        try {
            builder.addDisallowedApplication(packageName)
        } catch (_: Exception) {}

        val tunFd: ParcelFileDescriptor = builder.establish() ?: return
        val configFile = File(cacheDir, "tproxy.yml").apply {
            writeText(
                """
                tunnel:
                  mtu: 1500
                  ipv4: 10.0.0.2
                  ipv6: 'fd00::2'
                socks5:
                  port: \$socksPort
                  address: 127.0.0.1
                  udp: 'udp'
                """.trimIndent()
            )
        }
        Tun2Socks.start(configFile.absolutePath, tunFd)
    }

    override fun onDestroy() {
        Tun2Socks.stop()
        super.onDestroy()
    }

    companion object {
        const val ACTION_STOP = "app.mozillavpnlite.STOP"

        @Volatile
        var isRunning = false
            private set

        fun dataDir(context: Context): File = File(context.filesDir, "core")

        @Synchronized
        fun startCore(context: Context, on: Boolean) {
            mobile.Mobile.start(dataDir(context).absolutePath, on)
        }

        fun turnOn(context: Context) {
            val intent = Intent(context, MinimalVpnService::class.java)
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
                context.startForegroundService(intent)
            } else {
                context.startService(intent)
            }
            startCore(context, on = true)
            mobile.Mobile.connect()
            isRunning = true
        }

        fun turnOff(context: Context) {
            Tun2Socks.stop()
            mobile.Mobile.disconnect()
            isRunning = false
            context.stopService(Intent(context, MinimalVpnService::class.java))
        }
    }
}
`
  },
  {
    path: 'app/src/main/AndroidManifest.xml',
    language: 'xml',
    title: 'AndroidManifest.xml (Минимальные разрешения)',
    description: 'Манифест Android-приложения: только INTERNET, FOREGROUND_SERVICE и BIND_VPN_SERVICE.',
    content: `<?xml version="1.0" encoding="utf-8"?>
<manifest xmlns:android="http://schemas.android.com/apk/res/android"
    package="app.mozillavpnlite">

    <uses-permission android:name="android.permission.INTERNET" />
    <uses-permission android:name="android.permission.ACCESS_NETWORK_STATE" />
    <uses-permission android:name="android.permission.FOREGROUND_SERVICE" />
    <uses-permission android:name="android.permission.FOREGROUND_SERVICE_SPECIAL_USE" />

    <application
        android:label="Mozilla VPN Lite"
        android:allowBackup="false"
        android:supportsRtl="true"
        android:theme="@android:style/Theme.DeviceDefault.NoActionBar">

        <activity
            android:name=".MainActivity"
            android:exported="true"
            android:launchMode="singleTask">
            <intent-filter>
                <action android:name="android.intent.action.MAIN" />
                <category android:name="android.intent.category.LAUNCHER" />
            </intent-filter>
        </activity>

        <service
            android:name=".MinimalVpnService"
            android:exported="false"
            android:permission="android.permission.BIND_VPN_SERVICE"
            android:foregroundServiceType="specialUse">
            <intent-filter>
                <action android:name="android.net.VpnService" />
            </intent-filter>
        </service>
    </application>
</manifest>
`
  },
  {
    path: '.github/workflows/build-apk.yml',
    language: 'markdown',
    title: '.github/workflows/build-apk.yml (Сборка переписанного Go-ядра и APK)',
    description: 'Автономный пайплайн GitHub Actions: заменяет тяжеловесное ядро на наше переписанное (core/), компилирует core.aar через gomobile bind и выпускает готовый Mozilla-VPN-Lite-arm64-v8a.apk в Releases.',
    content: `name: Build Mozilla VPN Lite Android APK

on:
  push:
    branches: [ main, master ]
  workflow_dispatch:

permissions:
  contents: write

env:
  FLUTTER_VERSION: 3.47.7
  GOMOBILE_VERSION: v0.0.0-20260908204917-8b95e45f8d3e

jobs:
  build-android-apk:
    runs-on: ubuntu-24.04
    steps:
      - name: Checkout Mozilla VPN Lite repository (Rewritten Go Core + Minimal UI)
        uses: actions/checkout@v4
        with:
          path: lite

      - name: Clone Android Tun2Socks NDK harness
        run: |
          git clone --depth 1 https://github.com/armanlife/arman-vpn-client.git upstream
          rm -rf upstream/core
          cp -r lite/core upstream/core
          cp lite/patches/mobile_shell.dart upstream/app/lib/ui/mobile_shell.dart
          sed -i 's/android:label="[^"]*"/android:label="Mozilla VPN Lite"/g' upstream/app/android/app/src/main/AndroidManifest.xml || true

      - name: Set up Go
        uses: actions/setup-go@v5
        with:
          go-version: '1.23'

      - name: Set up Java 21
        uses: actions/setup-java@v4
        with:
          distribution: temurin
          java-version: '21'

      - name: Set up Flutter
        uses: subosito/flutter-action@v2
        with:
          flutter-version: \${{ env.FLUTTER_VERSION }}
          channel: stable
          cache: true

      - name: Install Android NDK & gomobile
        working-directory: upstream
        run: |
          ndk="\$(sed -n 's/.*ndkVersion = "\\(.*\\)".*/\\1/p' app/android/app/build.gradle.kts)"
          yes | "\$ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager" "ndk;\$ndk" > /dev/null
          echo "ANDROID_NDK_HOME=\$ANDROID_HOME/ndk/\$ndk" >> "\$GITHUB_ENV"
          go install "golang.org/x/mobile/cmd/gomobile@\$GOMOBILE_VERSION" "golang.org/x/mobile/cmd/gobind@\$GOMOBILE_VERSION"

      - name: Compile Rewritten Minimal Go Core (core.aar) & Build Release APKs
        working-directory: upstream
        run: |
          mkdir -p app/android/app/libs
          (cd core && go get golang.org/x/mobile/bind@\$GOMOBILE_VERSION && go mod tidy && \\
            gomobile bind -target=android -androidapi 24 -trimpath -ldflags "-s -w" \\
            -o ../app/android/app/libs/core.aar ./mobile)
          cd app
          flutter pub get
          mkdir -p ../../out
          flutter build apk --release --split-per-abi
          cp build/app/outputs/flutter-apk/app-arm64-v8a-release.apk ../../out/Mozilla-VPN-Lite-arm64-v8a.apk
          cp build/app/outputs/flutter-apk/app-armeabi-v7a-release.apk ../../out/Mozilla-VPN-Lite-armeabi-v7a.apk

      - name: Upload Ready Android APKs to Artifacts
        uses: actions/upload-artifact@v4
        with:
          name: Mozilla-VPN-Lite-Android-APK
          path: out/*.apk
          if-no-files-found: error

      - name: Create or Update Latest GitHub Release with APK
        env:
          GH_TOKEN: \${{ github.token }}
        run: |
          tag="v1.0.0-lite"
          gh release delete "\$tag" --yes --cleanup-tag 2>/dev/null || true
          gh release create "\$tag" out/*.apk \\
            --title "Mozilla VPN Lite (Android APK — Rewritten Minimal Core)" \\
            --notes "Минималистичная сборка с полностью переписанным Go-ядром (mozillavpnlite/core): только FxA, Guardian ProxyPass, пул аккаунтов 5 ГБ, Remote Settings и локальный туннель."
`
  }
];
