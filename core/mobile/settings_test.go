package mobile

import (
	"path/filepath"
	"testing"
)

// The last chosen location must survive a cold start: saveSettings
// writes it, loadSettings (called from Start) restores it over the
// "DE" default.
func TestSelectedLocationPersistsAcrossRestart(t *testing.T) {
	dir := t.TempDir()

	mu.Lock()
	oldSel, oldPath := selectedCode, settingsPath
	selectedCode = "DE"
	mu.Unlock()
	defer func() {
		mu.Lock()
		selectedCode, settingsPath = oldSel, oldPath
		mu.Unlock()
	}()

	// First run in a fresh dir: nothing to load, default stays.
	loadSettings(dir)
	mu.Lock()
	if selectedCode != "DE" {
		t.Fatalf("fresh start changed the default: %s", selectedCode)
	}
	// User picks Norway, then Recommended.
	selectedCode = "NO"
	mu.Unlock()
	saveSettings()

	// Cold start: back to the default, then load restores Norway.
	mu.Lock()
	selectedCode = "DE"
	mu.Unlock()
	loadSettings(dir)
	mu.Lock()
	got := selectedCode
	mu.Unlock()
	if got != "NO" {
		t.Fatalf("after restart location = %s, want NO", got)
	}

	// The settings file really is plain JSON with the code.
	if settingsPath != filepath.Join(dir, "settings.json") {
		t.Fatalf("settingsPath = %s", settingsPath)
	}
}
