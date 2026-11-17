package tunnel

import (
	"testing"

	"mozillavpnlite/core/geoip"
)

// The bypass decision: only IP literals inside the installed table go
// direct; hostnames, foreign IPs and a missing table never do.
func TestShouldBypass(t *testing.T) {
	e := NewEngine()
	if e.shouldBypass("77.88.8.8") {
		t.Fatal("no table installed, but shouldBypass is true")
	}
	tab, err := geoip.Load("RU")
	if err != nil {
		t.Fatal(err)
	}
	e.SetBypassTable(tab)
	for host, want := range map[string]bool{
		"77.88.8.8":   true,  // Yandex DNS, RU
		"93.158.134.3": true, // yandex.ru, RU
		"8.8.8.8":      false,
		"1.1.1.1":      false,
		"example.com":  false, // hostname: not decidable by address
		"":             false,
	} {
		if got := e.shouldBypass(host); got != want {
			t.Errorf("shouldBypass(%q) = %v, want %v", host, got, want)
		}
	}
	e.SetBypassTable(nil)
	if e.shouldBypass("77.88.8.8") {
		t.Error("table cleared, but shouldBypass is still true")
	}
}
