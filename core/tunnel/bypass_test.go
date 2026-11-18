package tunnel

import (
	"testing"

	"mozillavpnlite/core/geoip"
)

// The bypass decision: IP literals inside the installed table go
// direct, and local addresses always do; hostnames, foreign IPs
// and a missing table never send anything else direct.
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

// Local addresses bypass unconditionally — no table, a foreign
// table, it makes no difference: the home router and the LAN must
// stay reachable with the VPN up, and a proxy egress could never
// reach them anyway.
func TestShouldBypassLocalAlways(t *testing.T) {
	e := NewEngine()
	local := []string{
		"192.168.1.1",   // RFC 1918
		"10.0.0.5",      // RFC 1918
		"172.16.3.4",    // RFC 1918, bottom of /12
		"172.31.255.1",  // RFC 1918, top of /12
		"100.64.1.2",    // CGNAT, RFC 6598
		"127.0.0.1",     // loopback
		"169.254.10.20", // link-local
	}
	for _, host := range local {
		if !e.shouldBypass(host) {
			t.Errorf("shouldBypass(%q) = false, want true (local, no table)", host)
		}
	}
	// Just outside the private ranges stays proxied (no table).
	for _, host := range []string{"172.32.0.1", "100.128.0.1", "11.0.0.1"} {
		if e.shouldBypass(host) {
			t.Errorf("shouldBypass(%q) = true, want false (public, no table)", host)
		}
	}
	tab, err := geoip.Load("RU")
	if err != nil {
		t.Fatal(err)
	}
	e.SetBypassTable(tab)
	for _, host := range local {
		if !e.shouldBypass(host) {
			t.Errorf("shouldBypass(%q) = false, want true (local, RU table)", host)
		}
	}
}
