// Package accounts manages the pool of Mozilla accounts, credential
// persistence, instant 1-tap profile switching, background pre-warming of the
// standby account at (Threshold - 5%), and 0-ms automatic failover when an
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
	ID       string               `json:"id"`
	Email    string               `json:"email"`
	Position int                  `json:"position"`
	Active   bool                 `json:"active"`
	Enabled  bool                 `json:"enabled"`
	Status   Status               `json:"status"`
	Reason   string               `json:"reason,omitempty"`
	Tokens   fxa.OAuthTokens      `json:"tokens"`
	LastPass *guardian.ProxyPass  `json:"last_pass,omitempty"`
	Usage    *guardian.QuotaUsage `json:"usage,omitempty"`
}

func (a *Account) HasWarmPass() bool {
	return a != nil && a.LastPass != nil && a.LastPass.RawToken != "" &&
		time.Now().Before(a.LastPass.ExpiresAt.Add(-5*time.Minute))
}

type Pool struct {
	mu               sync.Mutex
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

// Activate immediately makes the given account ID active.
// If the account already has a warm ProxyPass in memory, it returns in 0 ms and
// refreshes quota in the background.
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
	warmPass := target.LastPass
	isWarm := target.HasWarmPass()
	_ = p.saveLocked()
	p.mu.Unlock()

	if isWarm {
		go func(acc *Account) {
			bgCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_, _ = p.EnsureFreshPass(bgCtx, acc)
		}(target)
		return warmPass, nil
	}

	return p.EnsureFreshPass(ctx, target)
}

// EnsureFreshPass returns a valid ProxyPass for acc, refreshing OAuth or rotating
// to the next account in the pool if acc's quota is exhausted.
// When usage reaches (ThresholdPercent - 5%), it pre-warms the next standby account in the background.
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
	threshold := float64(p.ThresholdPercent)
	if pass != nil && pass.Usage != nil {
		acc.Usage = pass.Usage
		acc.LastPass = pass
	}
	_ = p.saveLocked()
	p.mu.Unlock()

	if errors.Is(err, guardian.ErrQuotaExceeded) || (pass != nil && pass.Usage != nil && pass.Usage.UsedPercent() >= threshold) {
		return p.RotateToNextReady(ctx, acc.ID)
	}

	// Pre-warm standby account when active account crosses (Threshold - 5%)
	if pass != nil && pass.Usage != nil && pass.Usage.UsedPercent() >= (threshold-5.0) {
		go p.PrewarmNextReady(acc.ID)
	}

	return pass, err
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
	_, _ = p.EnsureFreshPass(ctx, standby)
}

// RotateToNextReady finds the next enabled account with remaining quota and activates it.
// If the standby account was already pre-warmed, returns its cached ProxyPass in 0 ms.
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
	warmPass := candidate.LastPass
	isWarm := candidate.HasWarmPass()
	_ = p.saveLocked()
	p.mu.Unlock()

	if isWarm {
		return warmPass, nil
	}
	return p.EnsureFreshPass(ctx, candidate)
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
		return errors.New("аккаунт не найден")
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
