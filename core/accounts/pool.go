// Package accounts manages the pool of Mozilla accounts, credential
// persistence, instant 1-tap profile switching, live traffic accounting,
// background pre-warming of the standby account at (Threshold - 5%), and
// 0-ms automatic failover when an account's 5 GB monthly quota reaches
// the configured threshold.
package accounts

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"mozillavpnlite/core/fxa"
	"mozillavpnlite/core/guardian"
	"mozillavpnlite/core/lang"
)

type Status string

const (
	StatusReady          Status = "ready"
	StatusReserved       Status = "reserved"
	StatusReauthRequired Status = "reauth_required"
	StatusNoPass         Status = "no_pass"
	StatusNotEntitled    Status = "not_entitled"
	StatusQuotaExceeded  Status = "quota_exceeded"
	StatusUnreachable    Status = "unreachable"
)

type Account struct {
	ID       string `json:"id"`
	Email    string `json:"email"`
	Position int    `json:"position"`
	Active   bool   `json:"active"`
	Enabled  bool   `json:"enabled"`
	Status   Status `json:"status"`
	Reason   string `json:"reason,omitempty"`
	// Health bookkeeping, written by every pass check (obtainPass):
	// when the account was last checked, when it last worked, what
	// failed last, how many failures in a row, and until when checks
	// of a failed account are paused. Before this existed, whole
	// classes of failure (Guardian-side refusals) were recorded
	// nowhere and a dead account kept a clean card in the UI.
	LastCheckAt   time.Time            `json:"last_check_at,omitempty"`
	LastOKAt      time.Time            `json:"last_ok_at,omitempty"`
	LastError     string               `json:"last_error,omitempty"`
	FailCount     int                  `json:"fail_count,omitempty"`
	CooldownUntil time.Time            `json:"cooldown_until,omitempty"`
	Tokens        fxa.OAuthTokens      `json:"tokens"`
	LastPass      *guardian.ProxyPass  `json:"last_pass,omitempty"`
	Usage         *guardian.QuotaUsage `json:"usage,omitempty"`
}

func (a *Account) HasWarmPass() bool {
	return a != nil && a.LastPass != nil && a.LastPass.RawToken != "" &&
		time.Now().Before(a.LastPass.ExpiresAt.Add(-5*time.Minute))
}

type Pool struct {
	mu               sync.Mutex
	refreshMu        sync.Mutex
	prewarming       bool
	filePath         string
	ThresholdPercent int        `json:"threshold_percent"`
	Accounts         []*Account `json:"accounts"`
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
	// Load the main file, falling back to the .bak copy saveLocked
	// keeps. A corrupt pool must NOT silently become an empty one:
	// the next save would cement the loss of every account, so when
	// a pool file exists but neither copy parses, fail loudly.
	tryLoad := func(path string) bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}
		fresh := &Pool{ThresholdPercent: 95, Accounts: []*Account{}}
		if json.Unmarshal(data, fresh) != nil {
			return false
		}
		p.Accounts = fresh.Accounts
		p.ThresholdPercent = fresh.ThresholdPercent
		return true
	}
	if !tryLoad(fp) && !tryLoad(fp+".bak") {
		if _, err := os.Stat(fp); err == nil {
			return nil, errors.New("accounts pool file is corrupt and no valid backup exists")
		}
		if _, err := os.Stat(fp + ".bak"); err == nil {
			return nil, errors.New("accounts pool backup is corrupt")
		}
		// Neither file exists: a fresh pool.
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
	// Atomic replace: write a temp file in the same directory, fsync,
	// then rename over the pool file, keeping the previous good file
	// as .bak. A crash mid-write can no longer truncate the only copy
	// of the pool; LoadOrCreatePool falls back to .bak if the main
	// file is ever unreadable.
	tmp := p.filePath + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if _, err := os.Stat(p.filePath); err == nil {
		_ = os.Rename(p.filePath, p.filePath+".bak")
	}
	return os.Rename(tmp, p.filePath)
}

func (p *Pool) recomputeLocked() {
	for i, acc := range p.Accounts {
		acc.Position = i + 1
		if acc.Usage == nil || acc.Usage.LimitBytes <= 0 {
			acc.Usage = guardian.DefaultQuotaUsage()
		}
		if acc.Usage.RemainingBytes < 0 {
			acc.Usage.RemainingBytes = 0
		}
		if acc.Usage.RemainingBytes > acc.Usage.LimitBytes {
			acc.Usage.RemainingBytes = acc.Usage.LimitBytes
		}
		if !acc.Usage.Unlimited && (acc.Usage.RemainingBytes <= 0 || acc.Usage.UsedPercent() >= float64(p.ThresholdPercent)) {
			// The quota-reserve mark may only overwrite a ready/reserved
			// state — never a health mark (reauth, no_pass, …): this
			// recompute runs on every save and every UI snapshot, and
			// erasing a health diagnosis here would resurrect dead
			// accounts on the card.
			if len(p.Accounts) > 1 && (acc.Status == StatusReady || acc.Status == StatusReserved) {
				acc.Status = StatusReserved
				acc.Reason = lang.T("Достигнут порог месячной квоты", "Monthly quota threshold reached")
			}
		} else if acc.Status == StatusReserved {
			acc.Status = StatusReady
			acc.Reason = ""
		}
	}
}

// adoptUsageLocked takes the server's quota snapshot as-is. Local
// byte counting was removed at the user's request (2026-11-16):
// the server is the only source of quota truth now. During
// Mozilla's unlimited promo (Nov 2026 - Jan 2027) its snapshots
// carry Unlimited=true with full remaining, and the app honours
// that instead of counting bytes itself; when the promo ends the
// snapshots go back to real numbers and rotation resumes on them.
func adoptUsageLocked(acc *Account, incoming *guardian.QuotaUsage) {
	if incoming == nil {
		return
	}
	acc.Usage = incoming
}

// Flush persists the pool now.
func (p *Pool) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	_ = p.saveLocked()
}

// UpdateQuotaFromHeaders adopts the active account's quota from
// X-Quota-* headers, when present (including the unlimited flag).
func (p *Pool) UpdateQuotaFromHeaders(h http.Header) {
	parsed := guardian.ParseQuotaHeaders(h)
	if parsed == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.Accounts {
		if a.Active {
			adoptUsageLocked(a, parsed)
			_ = p.saveLocked()
			break
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

	usage := pass.Usage
	if usage == nil || usage.LimitBytes <= 0 {
		usage = guardian.DefaultQuotaUsage()
	}

	acc := &Account{
		ID:       "acc-" + hex.EncodeToString(idBytes[:]),
		Email:    email,
		Active:   len(p.Accounts) == 0,
		Enabled:  true,
		Status:   StatusReady,
		Tokens:   *tokens,
		LastPass: pass,
		Usage:    usage,
	}
	p.Accounts = append(p.Accounts, acc)
	_ = p.saveLocked()
	return acc, nil
}

// Activate makes the given account ID active and returns its pass.
// A reauth-required account is refused outright: it is lost without a
// fresh sign-in, and flagging it active would make the UI name a dead
// account as the connection's account. A cold account is fetched
// FIRST and the Active flags flip only on success — flag-first left
// every failed «В РАБОТУ» with a dead account shown as active. The
// warm path keeps the instant flag-first behaviour (its pass is
// already proven) with a background refresh as before.
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
		return nil, errors.New(lang.T("аккаунт не найден", "account not found"))
	}
	reauth := target.Status == StatusReauthRequired
	warmPass := target.LastPass
	isWarm := target.HasWarmPass()
	p.mu.Unlock()

	if reauth {
		return nil, fxa.ErrReauthRequired
	}
	if !isWarm {
		// obtainPass, not EnsureFreshPass: an explicit user pick is
		// honoured as-is — no quota rotation away from the chosen
		// account. Health is recorded inside either way.
		pass, err := p.obtainPass(ctx, target)
		if err != nil {
			return nil, err
		}
		p.mu.Lock()
		for _, a := range p.Accounts {
			a.Active = (a.ID == id)
		}
		target.Enabled = true
		_ = p.saveLocked()
		p.mu.Unlock()
		return pass, nil
	}

	p.mu.Lock()
	for _, a := range p.Accounts {
		a.Active = (a.ID == id)
	}
	target.Enabled = true
	_ = p.saveLocked()
	p.mu.Unlock()

	go func(acc *Account) {
		bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_, _ = p.EnsureFreshPass(bgCtx, acc)
	}(target)
	return warmPass, nil
}

// Health classes for pass-check outcomes. Hard classes condemn the
// account on the first failure; the soft class (unreachable) needs two
// consecutive failures, so one bad-network moment never marks a
// healthy account.
const (
	HealthOK           = ""
	HealthReauth       = "reauth"
	HealthQuota        = "quota"
	HealthNoPass       = "no_pass"
	HealthNotEntitled  = "not_entitled"
	HealthTokenInvalid = "token_invalid"
	HealthUnreachable  = "unreachable"
)

// HealthClass maps a pass-fetch error to its health class.
func HealthClass(err error) string {
	switch {
	case err == nil:
		return HealthOK
	case errors.Is(err, fxa.ErrReauthRequired):
		return HealthReauth
	case errors.Is(err, guardian.ErrQuotaExceeded):
		return HealthQuota
	case errors.Is(err, guardian.ErrEmptyToken):
		return HealthNoPass
	case errors.Is(err, guardian.ErrNotEntitled):
		return HealthNotEntitled
	case errors.Is(err, guardian.ErrTokenInvalid):
		return HealthTokenInvalid
	default:
		return HealthUnreachable
	}
}

// cooldownFor is the recheck pause after n consecutive failures: a
// quarter hour first, then hours. A dead account must not be hammered
// — and Guardian rate limits punish chatty clients.
func cooldownFor(failCount int) time.Duration {
	switch {
	case failCount <= 2:
		return 15 * time.Minute
	case failCount == 3:
		return time.Hour
	default:
		return 6 * time.Hour
	}
}

// recordCheckLocked stores a pass-check outcome in the account's
// health fields. Caller must hold p.mu and save the pool afterwards.
func (p *Pool) recordCheckLocked(acc *Account, class string, err error) {
	now := time.Now()
	acc.LastCheckAt = now
	if class == HealthOK {
		acc.LastOKAt = now
		acc.FailCount = 0
		acc.LastError = ""
		acc.CooldownUntil = time.Time{}
		switch acc.Status {
		case StatusNoPass, StatusNotEntitled, StatusQuotaExceeded, StatusUnreachable, StatusReauthRequired:
			acc.Status = StatusReady
			acc.Reason = ""
		}
		return
	}
	acc.FailCount++
	if err != nil {
		acc.LastError = err.Error()
	}
	switch class {
	case HealthReauth:
		acc.Status = StatusReauthRequired
		acc.Reason = lang.T("Требуется повторный вход", "Sign-in required")
		// Out of rotation until a fresh sign-in: rotation candidates
		// already skip non-ready statuses, and Enabled=false makes
		// the card stop claiming «в ротации» for a lost account.
		acc.Enabled = false
	case HealthTokenInvalid:
		// obtainPass already forced one token refresh + retry before
		// this verdict: the token chain is dead, only a fresh sign-in
		// heals it, so the user-facing state is the same as reauth.
		acc.Status = StatusReauthRequired
		acc.Reason = lang.T("Guardian отклонил токен — нужен повторный вход", "Guardian rejected the token — sign in again")
		acc.Enabled = false
	case HealthQuota:
		acc.Status = StatusQuotaExceeded
		acc.Reason = lang.T("Месячная квота исчерпана", "Monthly quota exhausted")
		acc.CooldownUntil = now.Add(6 * time.Hour)
	case HealthNotEntitled:
		acc.Status = StatusNotEntitled
		acc.Reason = lang.T("Аккаунт не активирован для VPN", "Account is not activated for VPN")
		acc.CooldownUntil = now.Add(cooldownFor(acc.FailCount))
	case HealthNoPass:
		acc.Status = StatusNoPass
		acc.Reason = lang.T("Guardian не выдал пропуск", "Guardian did not issue a pass")
		acc.CooldownUntil = now.Add(cooldownFor(acc.FailCount))
	default: // HealthUnreachable — soft, condemns only on the 2nd failure
		if acc.FailCount >= 2 {
			acc.Status = StatusUnreachable
			acc.Reason = lang.T("Временно недоступен", "Temporarily unreachable")
			acc.CooldownUntil = now.Add(cooldownFor(acc.FailCount))
		}
	}
}

// obtainPass fetches a fresh ProxyPass for acc (refreshing the OAuth
// token first when it is near expiry), records the outcome in the
// account's health fields, and returns the pass. It NEVER rotates the
// pool and NEVER reports a tokenless result as success. This is the
// single path every account check goes through — Connect, the token
// refresh loop, prewarm, the stale-account sweep — so health data is
// written no matter who asked. (Before this existed, a Guardian-side
// refusal was recorded nowhere: the switch died a second after being
// flipped while the account's card kept showing a clean state.)
func (p *Pool) obtainPass(ctx context.Context, acc *Account) (*guardian.ProxyPass, error) {
	// FxA refresh tokens rotate on every use: two concurrent refreshes
	// presenting the same token make the loser fail with invalid_grant,
	// which would wrongly mark a healthy account reauth-required.
	// Serialize refreshes pool-wide and re-check expiry under the lock —
	// when a sibling call (Activate's background refresh, Prewarm, the
	// 401 path) just refreshed, this call needs no network round-trip.
	p.refreshMu.Lock()
	if time.Now().After(acc.Tokens.ExpiresAt.Add(-2 * time.Minute)) {
		refreshed, err := fxa.RefreshAccessToken(ctx, acc.Tokens.RefreshToken)
		if err != nil {
			p.mu.Lock()
			p.recordCheckLocked(acc, HealthClass(err), err)
			_ = p.saveLocked()
			p.mu.Unlock()
			p.refreshMu.Unlock()
			return nil, err
		}
		p.mu.Lock()
		acc.Tokens = *refreshed
		_ = p.saveLocked()
		p.mu.Unlock()
	}
	p.refreshMu.Unlock()

	pass, err := guardian.FetchProxyPass(ctx, acc.Tokens.AccessToken)
	if errors.Is(err, guardian.ErrTokenInvalid) {
		// The stored access token can die ahead of its nominal expiry —
		// token chains get revoked when the account is used elsewhere.
		// Force one refresh and retry before condemning the account.
		if refreshed, rerr := fxa.RefreshAccessToken(ctx, acc.Tokens.RefreshToken); rerr == nil {
			p.mu.Lock()
			acc.Tokens = *refreshed
			_ = p.saveLocked()
			p.mu.Unlock()
			pass, err = guardian.FetchProxyPass(ctx, refreshed.AccessToken)
		}
	}

	if err == nil && pass != nil && pass.RawToken != "" {
		p.mu.Lock()
		// Never overwrite a good cached pass with an empty one: on
		// HTTP 429 Guardian returns a usage-only ProxyPass with no token.
		acc.LastPass = pass
		if pass.Usage != nil && !pass.Usage.Unlimited && pass.Usage.LimitBytes > 0 {
			adoptUsageLocked(acc, pass.Usage)
		} else if acc.Usage == nil || acc.Usage.LimitBytes <= 0 {
			acc.Usage = guardian.DefaultQuotaUsage()
		}
		p.recordCheckLocked(acc, HealthOK, nil)
		_ = p.saveLocked()
		p.mu.Unlock()
		return pass, nil
	}
	if err == nil {
		// HTTP 200 but no token in the body: Guardian's silent refusal.
		err = guardian.ErrEmptyToken
	}
	p.mu.Lock()
	p.recordCheckLocked(acc, HealthClass(err), err)
	_ = p.saveLocked()
	p.mu.Unlock()
	return nil, err
}

// EnsureFreshPass returns a valid ProxyPass for acc, rotating to the
// next account in the pool if acc's quota is exhausted (Guardian's
// verdict or the local counter reaching the threshold). Health
// recording happens inside obtainPass.
func (p *Pool) EnsureFreshPass(ctx context.Context, acc *Account) (*guardian.ProxyPass, error) {
	pass, err := p.obtainPass(ctx, acc)
	if err != nil {
		if errors.Is(err, guardian.ErrQuotaExceeded) {
			return p.RotateToNextReady(ctx, acc.ID)
		}
		return nil, err
	}
	p.mu.Lock()
	threshold := float64(p.ThresholdPercent)
	usage := acc.Usage
	p.mu.Unlock()
	if usage != nil && usage.UsedPercent() >= threshold {
		return p.RotateToNextReady(ctx, acc.ID)
	}
	if usage != nil && usage.UsedPercent() >= (threshold-5.0) {
		go p.PrewarmNextReady(acc.ID)
	}
	return pass, nil
}

// CheckAccount runs a health check for one account: a pass fetch with
// health recording and no pool rotation. Used by the stale-account
// sweep when the app opens.
func (p *Pool) CheckAccount(ctx context.Context, acc *Account) error {
	_, err := p.obtainPass(ctx, acc)
	return err
}

// SetActive flips the Active flags to the given account without any
// network work (the caller already holds a fresh pass for it).
func (p *Pool) SetActive(id string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.Accounts {
		a.Active = a.ID == id
		if a.Active {
			a.Enabled = true
		}
	}
	_ = p.saveLocked()
}

// FailoverCandidates lists accounts worth trying after the active one,
// in pool order: enabled, ready, and not in a health cooldown.
func (p *Pool) FailoverCandidates(excludeID string) []*Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []*Account
	now := time.Now()
	for _, a := range p.Accounts {
		if a.ID == excludeID || !a.Enabled || a.Status != StatusReady {
			continue
		}
		if now.Before(a.CooldownUntil) {
			continue
		}
		out = append(out, a)
	}
	return out
}

// ListAccounts returns the pool's live account pointers (a copy of
// the slice). For read-mostly iteration; mutations go through Pool
// methods.
func (p *Pool) ListAccounts() []*Account {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]*Account(nil), p.Accounts...)
}

// NeedsCheck reports whether the app-open sweep should re-check this
// account: a failed account whose cooldown has expired, or a ready
// account holding no warm pass — its quota snapshot is stale by
// definition (passes live ~10 minutes), so opening the app refreshes
// it, the way Arman VPN shows fresh quota state on open. The active
// account is included when it has no warm pass (e.g. the app was
// opened disconnected); reauth accounts need the user and are
// skipped.
func (a *Account) NeedsCheck(now time.Time) bool {
	if !a.Enabled {
		return false
	}
	switch a.Status {
	case StatusNoPass, StatusNotEntitled, StatusQuotaExceeded, StatusUnreachable:
		return !a.Active && !now.Before(a.CooldownUntil)
	case StatusReady:
		return !a.HasWarmPass()
	}
	return false
}

// PrewarmNextReady refreshes OAuth and caches a fresh ProxyPass JWT on the next
// ready standby account in the background so failover happens in 0 ms.
func (p *Pool) PrewarmNextReady(activeID string) {
	p.mu.Lock()
	if p.prewarming {
		p.mu.Unlock()
		return
	}
	var standby *Account
	for _, a := range p.Accounts {
		if a.ID != activeID && a.Enabled && a.Status == StatusReady {
			standby = a
			break
		}
	}
	if standby == nil || standby.HasWarmPass() {
		p.mu.Unlock()
		return
	}
	p.prewarming = true
	p.mu.Unlock()

	defer func() {
		p.mu.Lock()
		p.prewarming = false
		p.mu.Unlock()
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	// obtainPass, not EnsureFreshPass: prewarming a standby must never
	// rotate the pool's active account as a side effect.
	_, _ = p.obtainPass(ctx, standby)
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
		return nil, errors.New(lang.T("нет доступных резервных аккаунтов со свободной квотой", "no spare accounts with free quota available"))
	}
	for _, a := range p.Accounts {
		a.Active = (a.ID == candidate.ID)
	}
	warmPass := candidate.LastPass
	isWarm := candidate.HasWarmPass()
	_ = p.saveLocked()
	p.mu.Unlock()

	if isWarm {
		return warmPass, nil
	}
	return p.EnsureFreshPass(ctx, candidate)
}

func (p *Pool) ToggleEnabled(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, a := range p.Accounts {
		if a.ID == id {
			a.Enabled = !a.Enabled
			if !a.Enabled && a.Active {
				a.Active = false
				for _, other := range p.Accounts {
					if other.ID != id && other.Enabled {
						other.Active = true
						break
					}
				}
			}
			return p.saveLocked()
		}
	}
	return nil
}

func (p *Pool) Move(id string, delta int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	idx := -1
	for i, a := range p.Accounts {
		if a.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return errors.New(lang.T("аккаунт не найден", "account not found"))
	}
	next := idx + delta
	if next >= 0 && next < len(p.Accounts) {
		p.Accounts[idx], p.Accounts[next] = p.Accounts[next], p.Accounts[idx]
	}
	return p.saveLocked()
}

func (p *Pool) Remove(id string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	for i, a := range p.Accounts {
		if a.ID == id {
			wasActive := a.Active
			p.Accounts = append(p.Accounts[:i], p.Accounts[i+1:]...)
			if wasActive && len(p.Accounts) > 0 {
				p.Accounts[0].Active = true
			}
			return p.saveLocked()
		}
	}
	return nil
}

func (p *Pool) SetThreshold(percent int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if percent < 50 {
		percent = 50
	}
	if percent > 100 {
		percent = 100
	}
	p.ThresholdPercent = percent
	return p.saveLocked()
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

func (p *Pool) SnapshotAccounts() ([]*Account, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.recomputeLocked()
	// Deep-copy: callers JSON-marshal the snapshot while EnsureFreshPass
	// and the header callbacks keep mutating the live accounts — sharing pointers
	// here was a data race.
	out := make([]*Account, 0, len(p.Accounts))
	for _, a := range p.Accounts {
		cp := *a
		if a.Usage != nil {
			usage := *a.Usage
			cp.Usage = &usage
		}
		if a.LastPass != nil {
			lp := *a.LastPass
			if a.LastPass.Usage != nil {
				lpUsage := *a.LastPass.Usage
				lp.Usage = &lpUsage
			}
			cp.LastPass = &lp
		}
		out = append(out, &cp)
	}
	return out, p.ThresholdPercent
}
