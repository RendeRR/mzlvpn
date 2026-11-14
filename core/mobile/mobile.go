// Package mobile is the gomobile bind entry point for Android (libs/core.aar).
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
