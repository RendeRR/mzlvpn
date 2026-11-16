package mobile

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// settings.go — persists the user's server choice across cold starts.
// selectedCode used to live only in memory and reset to "DE" on every
// process start; the last chosen location (a country code, "REC" or
// "fastest") is now kept in settings.json next to the account pool.

var settingsPath string

type uiSettings struct {
	Location string `json:"location"`
}

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
	code := strings.TrimSpace(s.Location)
	if code == "" {
		return
	}
	mu.Lock()
	selectedCode = code
	mu.Unlock()
}

func saveSettings() {
	path := settingsPath
	if path == "" {
		return
	}
	mu.Lock()
	code := selectedCode
	mu.Unlock()
	data, err := json.Marshal(uiSettings{Location: code})
	if err != nil {
		return
	}
	_ = os.WriteFile(path, data, 0600)
}
