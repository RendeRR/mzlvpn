package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"mozillavpnlite/core/geoip"
)

// settings.go — persists the user's server choice across cold starts.
// selectedCode used to live only in memory and reset to "DE" on every
// process start; the last chosen location (a country code, "REC" or
// "fastest") is now kept in settings.json next to the account pool.

var settingsPath string

type uiSettings struct {
	Location    string `json:"location"`
	Bypass      bool   `json:"bypass"`
	HomeCountry string `json:"home_country"`
}

// Home-country bypass state (see SetBypassEnabled / SetHomeCountry).
// bypassEnabled is the user's toggle; homeCountry is the country whose
// addresses leave directly. Defaults: off, "RU".
var (
	bypassEnabled = false
	homeCountry   = "RU"
)

func loadSettings(dataDir string) {
	settingsPath = filepath.Join(dataDir, "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		return
	}
	var s uiSettings
	if err := json.Unmarshal(data, &s); err != nil {
		return
	}
	mu.Lock()
	if code := strings.TrimSpace(s.Location); code != "" {
		selectedCode = code
	}
	bypassEnabled = s.Bypass
	if cc := strings.ToUpper(strings.TrimSpace(s.HomeCountry)); cc != "" {
		homeCountry = cc
	}
	mu.Unlock()
	applyBypass()
}

func saveSettings() {
	path := settingsPath
	if path == "" {
		return
	}
	mu.Lock()
	s := uiSettings{Location: selectedCode, Bypass: bypassEnabled, HomeCountry: homeCountry}
	mu.Unlock()
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}

// applyBypass pushes the current bypass state into the tunnel engine:
// the home country's table when enabled, nil when off (or when the
// country has no data, which simply means "no bypass").
func applyBypass() {
	mu.Lock()
	en, cc := bypassEnabled, homeCountry
	mu.Unlock()
	if !en {
		engine.SetBypassTable(nil)
		return
	}
	t, err := geoip.Load(cc)
	if err != nil {
		engine.SetBypassTable(nil)
		return
	}
	engine.SetBypassTable(t)
}
