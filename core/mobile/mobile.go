// Package mobile is the gomobile bind entry point for Android (libs/core.aar).
// Contains ONLY Mozilla account management (with 2FA/email code & WebChannel support),
// automatic 5 GB quota rotation, Remote Settings server selection with real 1-RTT
// latency measurement, and the local SOCKS5 -> H2 CONNECT tunnel.
package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mozillavpnlite/core/accounts"
	"mozillavpnlite/core/fxa"
	"mozillavpnlite/core/guardian"
	"mozillavpnlite/core/lang"
	"mozillavpnlite/core/serverlist"
	"mozillavpnlite/core/tunnel"
)

var (
	mu           sync.Mutex
	refreshMu    sync.Mutex
	reconnectRun atomic.Bool
	failoverUsed atomic.Bool // one mid-session failover per connection
	pool         *accounts.Pool
	engine       = tunnel.NewEngine()
	locations    = serverlist.DefaultLocations()
	selectedCode = "DE"
	recLocation  = serverlist.DefaultRecommended()
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

	// tokenLoopStop closes the proactive pass-refresh loop; nil when
	// the loop is not running. Guarded by mu.
	tokenLoopStop chan struct{}
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

// SetLanguage selects the language of the core's user-facing messages
// ("en*" → English, anything else → Russian). Safe to call at any time.
func SetLanguage(code string) {
	lang.Set(code)
}

// Start initializes the account pool and server list in dataDir.
// language is the app's active UI language code (see SetLanguage).
func Start(dataDir string, autoConnect bool, language string) error {
	lang.Set(language)
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

	loadEvents(dataDir)
	loadSettings(dataDir)
	go sweepAccounts()

	// No local traffic counting (removed 2026-11-16 at the user's
	// request): quota state comes only from the server's snapshots
	// and headers, and quota rotation is driven by the proxy's own
	// exhaustion signal below plus the refresh path's threshold
	// check against the server-reported usage.
	engine.OnQuotaHeaders = func(h http.Header) {
		p.UpdateQuotaFromHeaders(h)
	}
	engine.OnQuotaExhaust = func() {
		rotateActiveAccount(p)
	}
	engine.OnAuthFailure = func() {
		refreshEngineToken("401")
	}

	// Automatically fetch all 32 countries and measure real 1-RTT pings from the device
	go func() {
		// 1. Measure real pings for initial list immediately (~1.5s)
		_ = PingAllLocations()

		// 2. Fetch full Remote Settings server list and measure real pings for all 32 locations
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		list, anycast, fetchErr := serverlist.FetchAll(ctx)
		cancel()
		if fetchErr == nil && len(list) > 0 {
			pingCtx, pingCancel := context.WithTimeout(context.Background(), 12*time.Second)
			measured := serverlist.MeasureLatenciesProgressive(
				pingCtx,
				list,
				func(c context.Context, endpoint string) (time.Duration, error) {
					return tunnel.ProbeTLSHandshake(c, endpoint)
				},
				func(snap []serverlist.Location) {
					mu.Lock()
					locations = snap
					mu.Unlock()
				},
			)
			pingCancel()
			mu.Lock()
			locations = measured
			recLocation = anycast
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

// refreshEngineToken fetches a fresh ProxyPass for the active account
// and hot-swaps it into the running engine. It is the single refresh
// path for every trigger: a 401 from the proxy (the held token died,
// typically while the device slept), a wake/network reconnect, and the
// background refresh after a warm Connect. A tokenless result (Guardian
// 429 usage-only reply) never overwrites the held token. Serialized by
// refreshMu so overlapping triggers never race on Guardian.
func refreshEngineToken(reason string) {
	refreshMu.Lock()
	defer refreshMu.Unlock()

	mu.Lock()
	p := pool
	isConn := connected
	mu.Unlock()
	if p == nil || !isConn {
		return
	}
	active := p.ActiveAccount()
	if active == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	pass, err := p.EnsureFreshPass(ctx, active)
	if err != nil {
		// A hard failure of the active account (Guardian refuses it,
		// sign-in died, no pass issued) gets ONE mid-session failover;
		// soft network failures are absorbed by the tunnel's 401
		// wait-and-retry as before and never rotate on their own.
		switch accounts.HealthClass(err) {
		case accounts.HealthReauth, accounts.HealthNoPass, accounts.HealthNotEntitled, accounts.HealthTokenInvalid:
			tryMidSessionFailover(p, active)
		}
		return
	}
	if pass != nil && pass.RawToken != "" {
		if winner := p.ActiveAccount(); winner != nil && winner.ID != active.ID {
			recordEvent("switch", switchText(active, winner))
		}
		engine.UpdateToken(pass.RawToken)
	}
}

// tryMidSessionFailover moves a live tunnel off a hard-failed active
// account onto the next ready one, once per connection. The engine
// token is hot-swapped; sessions keep running on the new account.
func tryMidSessionFailover(p *accounts.Pool, failed *accounts.Account) {
	if !failoverUsed.CompareAndSwap(false, true) {
		return
	}
	for _, cand := range p.FailoverCandidates(failed.ID) {
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		pass, err := p.EnsureFreshPass(ctx, cand)
		cancel()
		if err != nil || pass == nil || pass.RawToken == "" {
			continue
		}
		winner := cand
		// Same reference-point rule as connectPass: compare against
		// the failed account, not the candidate.
		if w := p.ActiveAccount(); w != nil && w.ID != failed.ID {
			winner = w
		} else {
			p.SetActive(cand.ID)
		}
		engine.UpdateToken(pass.RawToken)
		recordEvent("switch", switchText(failed, winner))
		return
	}
	recordEvent("down", fmt.Sprintf(lang.T("Аккаунт %s недоступен, переключиться не на что", "Account %s is unavailable and there is no standby to switch to"), failed.Email))
}

// sweepAccounts re-checks accounts whose health or quota state is
// stale, once when the app starts: failed accounts whose cooldown
// has expired and ready accounts holding no warm pass (their quota
// snapshot is refreshed too). Strictly
// sequential and quiet — one pass fetch per account — with recoveries
// journaled. (No manual "check" buttons: the Accounts tab has enough
// controls already, per the user's design call.)
func sweepAccounts() {
	time.Sleep(10 * time.Second)
	mu.Lock()
	p := pool
	mu.Unlock()
	if p == nil {
		return
	}
	now := time.Now()
	for _, acc := range p.ListAccounts() {
		if !acc.NeedsCheck(now) {
			continue
		}
		wasDown := acc.Status != accounts.StatusReady
		ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
		err := p.CheckAccount(ctx, acc)
		cancel()
		if err == nil && wasDown {
			recordEvent("recovered", fmt.Sprintf(lang.T("Аккаунт снова работает: %s", "Account is back: %s"), acc.Email))
		}
		time.Sleep(2 * time.Second)
	}
}

// startTokenRefreshLoop keeps the held ProxyPass from ever reaching
// its expiry while connected: passes live only ~10 minutes (measured
// in the field, 2026-11-15), so without this every long session —
// and every wake after a >10-minute sleep — met a dead token. The
// loop checks once a minute and refreshes when under 3 minutes
// remain. It cannot run while the device sleeps (the process is
// frozen with everything else); the wake gap is covered by the
// reconnect refresh and the tunnel's wait-and-retry on 401.
func startTokenRefreshLoop() {
	mu.Lock()
	if tokenLoopStop != nil {
		close(tokenLoopStop)
	}
	stop := make(chan struct{})
	tokenLoopStop = stop
	mu.Unlock()
	go func() {
		ticker := time.NewTicker(time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
			mu.Lock()
			p := pool
			isConn := connected
			mu.Unlock()
			if !isConn || p == nil {
				return
			}
			if active := p.ActiveAccount(); active != nil && active.LastPass != nil &&
				time.Until(active.LastPass.ExpiresAt) < 3*time.Minute {
				refreshEngineToken("expiry")
			}
		}
	}()
}

func stopTokenRefreshLoop() {
	mu.Lock()
	if tokenLoopStop != nil {
		close(tokenLoopStop)
		tokenLoopStop = nil
	}
	mu.Unlock()
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
		if winner := p.ActiveAccount(); winner != nil && winner.ID != active.ID {
			recordEvent("switch", switchText(active, winner))
		}
	}
}

// switchText formats a failover journal entry: where the tunnel moved
// and why. The reason comes from the failed account's health record.
func switchText(from, to *accounts.Account) string {
	reason := from.Reason
	if reason == "" {
		reason = from.LastError
	}
	if reason != "" {
		return fmt.Sprintf(lang.T("Переключено на %s: %s недоступен (%s)", "Switched to %s: %s is unavailable (%s)"), to.Email, from.Email, reason)
	}
	return fmt.Sprintf(lang.T("Переключено на %s: %s недоступен", "Switched to %s: %s is unavailable"), to.Email, from.Email)
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
		return errors.New(lang.T("ядро не инициализировано", "core is not initialized"))
	}
	active := p.ActiveAccount()
	if active == nil {
		return errors.New(lang.T("добавьте хотя бы один аккаунт Mozilla во вкладке Аккаунты", "add at least one Mozilla account in the Accounts tab"))
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
		failoverUsed.Store(false)
		go probeTunnel()
		go refreshEngineToken("connect")
		startTokenRefreshLoop()
		return nil
	}

	pass, err := connectPass(p, active)
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
	go probeTunnel()
	startTokenRefreshLoop()
	return nil
}

// connectPass obtains a pass for a Connect attempt: the active
// account first, then each failover candidate in pool order. A winner
// other than the previously active account becomes the active one and
// the switch is journaled — a connection that silently lands on a
// different account than the user picked is a bug, not a feature.
// Health outcomes are recorded inside the pool either way, so a failed
// active account finally shows its real state on its card.
func connectPass(p *accounts.Pool, active *accounts.Account) (*guardian.ProxyPass, error) {
	var lastErr error
	if active.Status == accounts.StatusReauthRequired {
		// Already condemned: a refresh round-trip would only repeat
		// the verdict and cost a second on every connect. Straight
		// to the candidates; the account revives via a fresh sign-in.
		lastErr = fxa.ErrReauthRequired
	} else {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		pass, err := p.EnsureFreshPass(ctx, active)
		cancel()
		if err == nil && pass != nil && pass.RawToken != "" {
			if winner := p.ActiveAccount(); winner != nil && winner.ID != active.ID {
				recordEvent("switch", switchText(active, winner))
			}
			failoverUsed.Store(false)
			return pass, nil
		}
		lastErr = err
	}
	for _, cand := range p.FailoverCandidates(active.ID) {
		cctx, ccancel := context.WithTimeout(context.Background(), 15*time.Second)
		cpass, cerr := p.EnsureFreshPass(cctx, cand)
		ccancel()
		if cerr != nil || cpass == nil || cpass.RawToken == "" {
			if cerr != nil {
				lastErr = cerr
			}
			continue
		}
		winner := cand
		// The reference point is the ORIGINAL active account, not the
		// candidate: flags count as "already set by an internal
		// rotation" only when ActiveAccount moved AWAY from it.
		// Comparing against the candidate misfires — the flags still
		// point at the failed account, SetActive is skipped, and the
		// tunnel then runs on the candidate's pass while the pool (and
		// the UI, the notification and the traffic accounting) keep
		// naming the dead account as active. Field-found 2026-11-16.
		if w := p.ActiveAccount(); w != nil && w.ID != active.ID {
			winner = w // rotated once more inside; flags already set
		} else {
			p.SetActive(cand.ID)
		}
		recordEvent("switch", switchText(active, winner))
		failoverUsed.Store(false)
		return cpass, nil
	}
	recordEvent("down", lang.T("Все аккаунты недоступны", "All accounts are unavailable"))
	if lastErr != nil {
		return nil, lastErr
	}
	return nil, errors.New(lang.T("ни один аккаунт не выдал пропуск", "no account could issue a pass"))
}

// probeTunnel exercises the data path right after Connect: one probe
// CONNECT through the fresh engine. If it fails, everything is flushed
// and the probe runs once more, so a dead-on-arrival connection is
// healed instead of surfacing as a browser error minutes later.
func probeTunnel() {
	for attempt := 0; attempt < 2; attempt++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		err := engine.Probe(ctx)
		cancel()
		if err == nil {
			return
		}
		engine.ResetSession()
	}
}

// Reconnect refreshes the token and probes the HTTP/2 session when Android switches
// networks (e.g. Wi-Fi <-> LTE/5G) or wakes on unlock. Coalesced via reconnectRun so
// back-to-back NetworkCallback + ACTION_USER_PRESENT events do not pile up.
func Reconnect() {
	if !reconnectRun.CompareAndSwap(false, true) {
		return
	}
	defer reconnectRun.Store(false)

	// Always refresh the pass on a wake/network reconnect, not only
	// when the stored pass looks stale: the engine's held token can be
	// older than the stored one, and a pass that dies while the device
	// sleeps otherwise keeps meeting CONNECTs with 401 until something
	// else happens to refresh it. Synchronous on purpose: the probe
	// below must not mistake a dead token for a dead session.
	refreshEngineToken("reconnect")
	// Verify before resetting. Most reconnect signals — a screen-off
	// of a few seconds, a network revalidation — leave the pooled
	// sessions perfectly alive, and a blind reset costs a redial
	// burst plus a new egress IP for every app (the field log showed
	// chkip getting a different IP on every refresh for ~8 s after
	// each unlock). Probe the current pool; reset only when the
	// probe proves it dead.
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	err := engine.Probe(ctx)
	cancel()
	if err == nil {
		return
	}
	engine.ResetSession()
}

// Disconnect stops the local SOCKS5 tunnel.
func Disconnect() {
	stopTokenRefreshLoop()
	failoverUsed.Store(false)
	engine.Stop()
	mu.Lock()
	connected = false
	p := pool
	mu.Unlock()
	if p != nil {
		p.Flush()
	}
}

// SwitchAccount immediately switches the active Mozilla account and hot-swaps
// its ProxyPass JWT in the running tunnel (0 ms if pre-warmed).
func SwitchAccount(accountID string) error {
	mu.Lock()
	p := pool
	mu.Unlock()
	if p == nil {
		return errors.New(lang.T("ядро не инициализировано", "core is not initialized"))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pass, err := p.Activate(ctx, accountID)
	if err == nil && (pass == nil || pass.RawToken == "") {
		err = guardian.ErrEmptyToken
	}
	if err != nil {
		email := accountID
		for _, a := range p.ListAccounts() {
			if a.ID == accountID {
				email = a.Email
				break
			}
		}
		recordEvent("fail", fmt.Sprintf(lang.T("Не удалось переключиться на %s: %s", "Could not switch to %s: %s"), email, err.Error()))
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
		return jsonResult("error", lang.T("Ядро ещё не инициализировано", "The core is not initialized yet"), email)
	}

	email = strings.TrimSpace(email)
	if email == "" || !strings.Contains(email, "@") {
		return jsonResult("error", lang.T("Введите корректный Email аккаунта Mozilla", "Enter a valid Mozilla account email"), email)
	}
	if password == "" {
		return jsonResult("error", lang.T("Введите пароль от аккаунта Mozilla", "Enter your Mozilla account password"), email)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 22*time.Second)
	defer cancel()

	loginResp, err := fxa.LoginWithPassword(ctx, email, password, unblockCode)
	if err != nil {
		var apiErr *fxa.APIError
		if errors.As(err, &apiErr) {
			if apiErr.Status == 406 {
				return jsonResult("need_browser", lang.T("Защита Fastly (HTTP 406) отклонила прямой запрос. Нажмите «Браузер».", "Fastly protection (HTTP 406) rejected the direct request. Tap “Browser”."), email)
			}
			if apiErr.Errno == 125 {
				_ = fxa.SendUnblockCode(ctx, email)
				return jsonResult("unblock_code", lang.T("Mozilla запросила код разблокировки входа (8 символов, отправлен вам на Email)", "Mozilla asked for a sign-in unblock code (8 characters, sent to your email)"), email)
			}
			if apiErr.Errno == 102 {
				return jsonResult("error", lang.T("Аккаунт с таким Email не найден в Mozilla", "No Mozilla account found for this email"), email)
			}
			if apiErr.Errno == 103 {
				return jsonResult("error", lang.T("Неверный пароль от аккаунта Mozilla", "Wrong Mozilla account password"), email)
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
			return jsonResult("verify_totp", lang.T("Введите 6-значный код из приложения-аутентификатора (2FA)", "Enter the 6-digit code from your authenticator app (2FA)"), canonicalEmail)
		}
		return jsonResult("verify_email", fmt.Sprintf(lang.T("Mozilla отправила 6-значный код подтверждения входа на %s. Введите его ниже:", "Mozilla sent a 6-digit sign-in confirmation code to %s. Enter it below:"), canonicalEmail), canonicalEmail)
	}

	if _, err := p.AddWithSessionToken(ctx, canonicalEmail, loginResp.SessionToken); err != nil {
		return jsonResult("error", lang.T("Ошибка выпуска ProxyPass: ", "ProxyPass issue failed: ")+err.Error(), canonicalEmail)
	}
	return jsonResult("ok", fmt.Sprintf(lang.T("Аккаунт %s успешно добавлен и активирован в Guardian", "Account %s added and activated in Guardian"), canonicalEmail), canonicalEmail)
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
		return jsonResult("error", lang.T("Нет активной сессии для подтверждения. Повторите вход.", "No active session to confirm. Please sign in again."), email)
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
				return jsonResult("error", lang.T("Код не принят Mozilla: ", "Mozilla did not accept the code: ")+err.Error(), email)
			}
		}
	} else {
		if verified, vErr := fxa.SessionVerified(ctx, sessionHex); vErr != nil || !verified {
			return jsonResult("error", lang.T("Введите 6-значный код подтверждения из письма Mozilla", "Enter the 6-digit confirmation code from the Mozilla email"), email)
		}
	}

	if _, err := p.AddWithSessionToken(ctx, email, sessionHex); err != nil {
		var apiErr *fxa.APIError
		if errors.As(err, &apiErr) && apiErr.Errno == 138 {
			return jsonResult("verify_email", lang.T("Сессия ещё не подтверждена. Введите 6-значный код из письма Mozilla:", "The session is not confirmed yet. Enter the 6-digit code from the Mozilla email:"), email)
		}
		return jsonResult("error", lang.T("Ошибка обмена OAuth / Guardian: ", "OAuth / Guardian exchange failed: ")+err.Error(), email)
	}

	mu.Lock()
	pendingSessionHex = ""
	mu.Unlock()

	return jsonResult("ok", fmt.Sprintf(lang.T("Аккаунт %s подтверждён и активирован в Guardian!", "Account %s confirmed and activated in Guardian!"), email), email)
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
		return jsonResult("error", lang.T("Ядро не инициализировано", "The core is not initialized"), email)
	}

	cleanToken := strings.TrimSpace(sessionTokenHex)
	if len(cleanToken) != 64 {
		return jsonResult("error", lang.T("SessionToken должен состоять из 64 hex-символов", "The sessionToken must be 64 hex characters"), email)
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
			fmt.Sprintf(lang.T("Mozilla отправила 6-значный код подтверждения входа на %s. Введите код из письма ниже:", "Mozilla sent a 6-digit sign-in confirmation code to %s. Enter the code from the email below:"), cleanEmail),
			cleanEmail,
		)
	}

	if _, err := p.AddWithSessionToken(ctx, cleanEmail, cleanToken); err != nil {
		var apiErr *fxa.APIError
		if errors.As(err, &apiErr) && apiErr.Errno == 138 {
			return jsonResult(
				"verify_email",
				fmt.Sprintf(lang.T("Mozilla отправила 6-значный код подтверждения входа на %s. Введите код из письма ниже:", "Mozilla sent a 6-digit sign-in confirmation code to %s. Enter the code from the email below:"), cleanEmail),
				cleanEmail,
			)
		}
		return jsonResult("error", lang.T("Не удалось обменять SessionToken: ", "Failed to exchange the sessionToken: ")+err.Error(), cleanEmail)
	}

	mu.Lock()
	pendingSessionHex = ""
	mu.Unlock()

	return jsonResult("ok", fmt.Sprintf(lang.T("Аккаунт %s успешно добавлен", "Account %s added"), cleanEmail), cleanEmail)
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

// SetBypassEnabled turns the home-country direct bypass on or off
// and persists the choice.
func SetBypassEnabled(enabled bool) {
	mu.Lock()
	bypassEnabled = enabled
	mu.Unlock()
	applyBypass()
	saveSettings()
}

// SetHomeCountry chooses which country's addresses bypass the tunnel
// (a geoip country code, e.g. "RU") and persists the choice.
func SetHomeCountry(code string) {
	code = strings.ToUpper(strings.TrimSpace(code))
	if code == "" {
		return
	}
	mu.Lock()
	homeCountry = code
	mu.Unlock()
	applyBypass()
	saveSettings()
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

	saveSettings()
	engine.SwitchServer(loc.Endpoint())
}

// PingAllLocations measures real 1-RTT latency across all Mozilla locations,
// updating the sorted list progressively as each Fastly node responds.
func PingAllLocations() error {
	mu.Lock()
	list := append([]serverlist.Location(nil), locations...)
	mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()

	measured := serverlist.MeasureLatenciesProgressive(
		ctx,
		list,
		func(c context.Context, endpoint string) (time.Duration, error) {
			return tunnel.ProbeTLSHandshake(c, endpoint)
		},
		func(snap []serverlist.Location) {
			mu.Lock()
			locations = snap
			mu.Unlock()
		},
	)

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
	bypassOn := bypassEnabled
	homeCC := homeCountry
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
		"bypass":            bypassOn,
		"home_country":      homeCC,
		"tunnel_protocol":   selProto,
		"location":          curLoc,
		"threshold_percent": threshold,
		"accounts":          accs,
		"locations":         locsCopy,
		"events":            eventsSnapshot(),
		"last_event":        lastEvent(),
	}
	data, _ := json.Marshal(payload)
	return string(data)
}

func currentLocationLocked() serverlist.Location {
	if strings.EqualFold(selectedCode, "REC") {
		// "Recommended" is Mozilla's real anycast endpoint: Fastly's
		// routing chooses the egress, not our ping table.
		return recLocation
	}
	if selectedCode == "" || strings.EqualFold(selectedCode, "fastest") {
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
