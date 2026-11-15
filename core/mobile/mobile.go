// Package mobile is the gomobile bind entry point for Android (libs/core.aar).
// Contains ONLY Mozilla account management (with 2FA/email code & WebChannel support),
// automatic 5 GB quota rotation, Remote Settings server selection with real 1-RTT
// latency measurement, and the local SOCKS5 -> H2 CONNECT tunnel.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
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
	locations    = serverlist.DefaultLocations()
	selectedCode = "DE"
	// Default is HTTP/2 CONNECT: HTTP/3 MASQUE (QUIC/UDP) is heavily
	// unreliable on many networks and often does not work in practice.
	// The tunnel engine always speaks HTTP/2 CONNECT; the protocol value
	// is currently a UI label kept for a future real MASQUE transport.
	selectedProtocol = "HTTP/2 CONNECT (TLS 1.3)"
	connected        = false

	// Pending 2-step login state when FxA requests an email/TOTP verification code
	pendingEmail      string
	pendingSessionHex string
	pendingVerifyMode string // "email-2fa" or "totp-2fa"
)

type ActionResult struct {
	State   string `json:"state"`   // "ok", "verify_email", "verify_totp", "unblock_code", "need_browser", "error"
	Message string `json:"message"` // Human-readable Russian status or error
	Email   string `json:"email,omitempty"`
}

func jsonResult(state, message, email string) string {
	b, _ := json.Marshal(ActionResult{State: state, Message: message, Email: email})
	return string(b)
}

// Start initializes the account pool and server list in dataDir.
func Start(dataDir string, autoConnect bool) error {
	mu.Lock()
	if pool != nil {
		mu.Unlock()
		if autoConnect {
			return Connect()
		}
		return nil
	}
	p, err := accounts.LoadOrCreatePool(dataDir)
	if err != nil {
		mu.Unlock()
		return err
	}
	pool = p
	mu.Unlock()

	engine.OnTrafficBytes = func(delta int64) {
		if p.RecordTraffic(delta) {
			go rotateActiveAccount(p)
		}
	}
	engine.OnQuotaHeaders = func(h http.Header) {
		p.UpdateQuotaFromHeaders(h)
	}
	engine.OnQuotaExhaust = func() {
		rotateActiveAccount(p)
	}

	// Automatically fetch all 32 countries and measure real 1-RTT pings from the device
	go func() {
		// 1. Measure real pings for initial list immediately (~1.5s)
		_ = PingAllLocations()

		// 2. Fetch full Remote Settings server list and measure real pings for all 32 locations
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		list, fetchErr := serverlist.Fetch(ctx)
		cancel()
		if fetchErr == nil && len(list) > 0 {
			pingCtx, pingCancel := context.WithTimeout(context.Background(), 20*time.Second)
			measured := serverlist.MeasureLatencies(pingCtx, list, func(c context.Context, endpoint string) (time.Duration, error) {
				return tunnel.ProbeTLSHandshake(c, endpoint)
			})
			pingCancel()
			mu.Lock()
			locations = measured
			mu.Unlock()
		}
	}()

	// Refresh active account quota in background on startup
	go func() {
		if active := p.ActiveAccount(); active != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			_, _ = p.EnsureFreshPass(ctx, active)
		}
	}()

	if autoConnect {
		return Connect()
	}
	return nil
}

func rotateActiveAccount(p *accounts.Pool) {
	active := p.ActiveAccount()
	if active == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if pass, err := p.RotateToNextReady(ctx, active.ID); err == nil && pass != nil {
		engine.UpdateToken(pass.RawToken)
	}
}

// Running reports whether the core has been initialized.
func Running() bool {
	mu.Lock()
	defer mu.Unlock()
	return pool != nil
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
		return errors.New("добавьте хотя бы один аккаунт Mozilla во вкладке Аккаунты")
	}

	// Start local SOCKS5 listener immediately using warm token if available
	var initialToken string
	if active.LastPass != nil && active.LastPass.RawToken != "" {
		initialToken = active.LastPass.RawToken
	}

	if initialToken != "" && active.HasWarmPass() {
		if _, err := engine.Start("127.0.0.1:0", loc.Endpoint(), initialToken); err != nil {
			return err
		}
		mu.Lock()
		connected = true
		mu.Unlock()

		// Refresh quota in the background without delaying tunnel startup
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
			defer cancel()
			if pass, err := p.EnsureFreshPass(ctx, active); err == nil && pass != nil {
				engine.UpdateToken(pass.RawToken)
			}
		}()
		return nil
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

// Reconnect immediately resets the HTTP/2 session when Android switches networks
// (e.g. Wi-Fi <-> LTE/5G) without waiting for old TCP timeouts.
func Reconnect() {
	engine.ResetSession()
	mu.Lock()
	p := pool
	isConn := connected
	mu.Unlock()
	if !isConn || p == nil {
		return
	}
	go func() {
		if active := p.ActiveAccount(); active != nil && !active.HasWarmPass() {
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if pass, err := p.EnsureFreshPass(ctx, active); err == nil && pass != nil {
				engine.UpdateToken(pass.RawToken)
			}
		}
	}()
}

// Disconnect stops the local SOCKS5 tunnel.
func Disconnect() {
	engine.Stop()
	mu.Lock()
	connected = false
	mu.Unlock()
}

// SwitchAccount immediately switches the active Mozilla account and hot-swaps
// its ProxyPass JWT in the running tunnel (0 ms if pre-warmed).
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

// SignInPassword signs in with email + password (PBKDF2 + HKDF + HAWK) and returns a JSON ActionResult.
func SignInPassword(email, password, unblockCode string) string {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p == nil {
		return jsonResult("error", "Ядро ещё не инициализировано", email)
	}

	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return jsonResult("error", "Введите корректный Email аккаунта Mozilla", email)
	}
	if password == "" {
		return jsonResult("error", "Введите пароль от аккаунта Mozilla", email)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()

	loginResp, err := fxa.LoginWithPassword(ctx, email, password, unblockCode)
	if err != nil {
		var apiErr *fxa.APIError
		if errors.As(err, &apiErr) {
			if apiErr.Status == 406 {
				return jsonResult("need_browser", "Защита Fastly (HTTP 406) отклонила прямой запрос. Нажмите «Браузер».", email)
			}
			if apiErr.Errno == 125 {
				_ = fxa.SendUnblockCode(ctx, email)
				return jsonResult("unblock_code", "Mozilla запросила код разблокировки входа (8 символов, отправлен вам на Email)", email)
			}
			if apiErr.Errno == 102 {
				return jsonResult("error", "Аккаунт с таким Email не найден в Mozilla", email)
			}
			if apiErr.Errno == 103 {
				return jsonResult("error", "Неверный пароль от аккаунта Mozilla", email)
			}
		}
		return jsonResult("error", err.Error(), email)
	}

	canonicalEmail := loginResp.Email
	if canonicalEmail == "" {
		canonicalEmail = email
	}

	if !loginResp.Verified {
		mu.Lock()
		pendingEmail = canonicalEmail
		pendingSessionHex = loginResp.SessionToken
		pendingVerifyMode = loginResp.VerificationMethod
		mu.Unlock()

		if loginResp.VerificationMethod == "totp-2fa" {
			return jsonResult("verify_totp", "Введите 6-значный код из приложения-аутентификатора (2FA)", canonicalEmail)
		}
		return jsonResult("verify_email", "Mozilla отправила 6-значный код подтверждения входа на "+canonicalEmail+". Введите его ниже:", canonicalEmail)
	}

	if _, err := p.AddWithSessionToken(ctx, canonicalEmail, loginResp.SessionToken); err != nil {
		return jsonResult("error", "Ошибка выпуска ProxyPass: "+err.Error(), canonicalEmail)
	}
	return jsonResult("ok", "Аккаунт "+canonicalEmail+" успешно добавлен и активирован в Guardian", canonicalEmail)
}

// VerifySignInCode confirms the pending sessionToken with the 6-digit code from email or 2FA authenticator.
func VerifySignInCode(code string) string {
	mu.Lock()
	p := pool
	email := pendingEmail
	sessionHex := pendingSessionHex
	mode := pendingVerifyMode
	mu.Unlock()

	if p == nil || sessionHex == "" {
		return jsonResult("error", "Нет активной сессии для подтверждения. Повторите вход.", email)
	}
	cleanCode := strings.TrimSpace(code)

	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()

	if cleanCode != "" {
		var err error
		if mode == "totp-2fa" {
			err = fxa.VerifySessionTOTP(ctx, sessionHex, cleanCode)
		} else {
			err = fxa.VerifySessionCode(ctx, sessionHex, cleanCode)
		}
		if err != nil {
			if verified, vErr := fxa.SessionVerified(ctx, sessionHex); vErr != nil || !verified {
				return jsonResult("error", "Код не принят Mozilla: "+err.Error(), email)
			}
		}
	} else {
		if verified, vErr := fxa.SessionVerified(ctx, sessionHex); vErr != nil || !verified {
			return jsonResult("error", "Введите 6-значный код подтверждения из письма Mozilla", email)
		}
	}

	if _, err := p.AddWithSessionToken(ctx, email, sessionHex); err != nil {
		var apiErr *fxa.APIError
		if errors.As(err, &apiErr) && apiErr.Errno == 138 {
			return jsonResult("verify_email", "Сессия ещё не подтверждена. Введите 6-значный код из письма Mozilla:", email)
		}
		return jsonResult("error", "Ошибка обмена OAuth / Guardian: "+err.Error(), email)
	}

	mu.Lock()
	pendingSessionHex = ""
	mu.Unlock()

	return jsonResult("ok", "Аккаунт "+email+" подтверждён и активирован в Guardian!", email)
}

// AddAccountPassword is kept for backward compatibility and delegates to SignInPassword.
func AddAccountPassword(email, password string) error {
	res := SignInPassword(email, password, "")
	var parsed ActionResult
	_ = json.Unmarshal([]byte(res), &parsed)
	if parsed.State != "ok" {
		return errors.New(parsed.Message)
	}
	return nil
}

// AddAccountSessionToken adds an account using a 64-hex FxA sessionToken from WebChannel or manual paste.
func AddAccountSessionToken(email, sessionTokenHex string) string {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p == nil {
		return jsonResult("error", "Ядро не инициализировано", email)
	}

	cleanToken := strings.TrimSpace(sessionTokenHex)
	if len(cleanToken) != 64 {
		return jsonResult("error", "SessionToken должен состоять из 64 hex-символов", email)
	}
	cleanEmail := strings.TrimSpace(email)
	if cleanEmail == "" {
		cleanEmail = "firefox.user@mozilla.org"
	}

	mu.Lock()
	pendingEmail = cleanEmail
	pendingSessionHex = cleanToken
	pendingVerifyMode = "email-2fa"
	mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	verified, err := fxa.SessionVerified(ctx, cleanToken)
	if err != nil || !verified {
		return jsonResult(
			"verify_email",
			"Mozilla отправила 6-значный код подтверждения входа на "+cleanEmail+". Введите код из письма ниже:",
			cleanEmail,
		)
	}

	if _, err := p.AddWithSessionToken(ctx, cleanEmail, cleanToken); err != nil {
		var apiErr *fxa.APIError
		if errors.As(err, &apiErr) && apiErr.Errno == 138 {
			return jsonResult(
				"verify_email",
				"Mozilla отправила 6-значный код подтверждения входа на "+cleanEmail+". Введите код из письма ниже:",
				cleanEmail,
			)
		}
		return jsonResult("error", "Не удалось обменять SessionToken: "+err.Error(), cleanEmail)
	}

	mu.Lock()
	pendingSessionHex = ""
	mu.Unlock()

	return jsonResult("ok", "Аккаунт "+cleanEmail+" успешно добавлен", cleanEmail)
}

func MoveAccount(accountID string, delta int) {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p != nil {
		_ = p.Move(accountID, delta)
	}
}

func ToggleAccountEnabled(accountID string) {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p != nil {
		_ = p.ToggleEnabled(accountID)
	}
}

func RemoveAccount(accountID string) {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p != nil {
		_ = p.Remove(accountID)
	}
}

func SetThreshold(percent int) {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p != nil {
		_ = p.SetThreshold(percent)
	}
}

func SetProtocol(proto string) {
	mu.Lock()
	selectedProtocol = proto
	mu.Unlock()
}

// SetLocation changes the active server country code and switches the upstream Fastly node.
func SetLocation(countryCode string) {
	mu.Lock()
	selectedCode = strings.TrimSpace(countryCode)
	loc := currentLocationLocked()
	mu.Unlock()

	engine.SwitchServer(loc.Endpoint())
}

// PingAllLocations measures real 1-RTT latency across all Mozilla locations.
func PingAllLocations() error {
	mu.Lock()
	list := append([]serverlist.Location(nil), locations...)
	mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	measured := serverlist.MeasureLatencies(ctx, list, func(c context.Context, endpoint string) (time.Duration, error) {
		return tunnel.ProbeTLSHandshake(c, endpoint)
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
	p := pool
	isConn := connected
	selCode := selectedCode
	selProto := selectedProtocol
	curLoc := currentLocationLocked()
	locsCopy := append([]serverlist.Location(nil), locations...)
	mu.Unlock()

	var accs []*accounts.Account
	threshold := 95
	if p != nil {
		accs, threshold = p.SnapshotAccounts()
	}

	payload := map[string]any{
		"connected":         isConn,
		"socks_port":        engine.Port(),
		"selected_location": selCode,
		"tunnel_protocol":   selProto,
		"location":          curLoc,
		"threshold_percent": threshold,
		"accounts":          accs,
		"locations":         locsCopy,
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func currentLocationLocked() serverlist.Location {
	if selectedCode == "" || strings.EqualFold(selectedCode, "REC") || strings.EqualFold(selectedCode, "fastest") {
		if len(locations) > 0 {
			return locations[0]
		}
	}
	for _, l := range locations {
		if strings.EqualFold(l.Code, selectedCode) {
			return l
		}
	}
	if len(locations) > 0 {
		return locations[0]
	}
	return serverlist.DefaultLocations()[0]
}
