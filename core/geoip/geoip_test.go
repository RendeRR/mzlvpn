package geoip

import (
	"net"
	"testing"
)

func TestCountriesAvailable(t *testing.T) {
	cs := Countries()
	if len(cs) != 20 {
		t.Fatalf("Countries() = %v, want 20 entries", cs)
	}
	found := false
	for _, c := range cs {
		if c == "RU" {
			found = true
		}
	}
	if !found {
		t.Fatalf("RU missing from %v", cs)
	}
}

func TestLoadAndContainsRU(t *testing.T) {
	tab, err := Load("RU")
	if err != nil {
		t.Fatal(err)
	}
	if tab.PrefixCount() < 5000 {
		t.Fatalf("RU prefixes = %d, suspiciously few", tab.PrefixCount())
	}
	for ip, want := range map[string]bool{
		"77.88.8.8":      true,  // Yandex DNS
		"93.158.134.3":   true,  // yandex.ru
		"217.69.139.202": true,  // mail.ru
		"8.8.8.8":        false, // Google DNS (US)
		"1.1.1.1":        false, // Cloudflare anycast
		"9.9.9.9":        false, // Quad9
	} {
		if got := tab.Contains(net.ParseIP(ip)); got != want {
			t.Errorf("RU.Contains(%s) = %v, want %v", ip, got, want)
		}
	}
	// IPv6 never matches the IPv4 tables.
	if tab.Contains(net.ParseIP("2a02:6b8::1")) {
		t.Error("IPv6 address matched an IPv4 table")
	}
}

func TestLoadUSAndUnknown(t *testing.T) {
	us, err := Load("US")
	if err != nil {
		t.Fatal(err)
	}
	if !us.Contains(net.ParseIP("8.8.8.8")) {
		t.Error("US table must contain 8.8.8.8")
	}
	if _, err := Load("XX"); err == nil {
		t.Error("Load(XX) must fail")
	}
}

func TestMergeRanges(t *testing.T) {
	rs := mergeRanges([]ipRange{{10, 20}, {15, 25}, {26, 30}, {100, 110}, {50, 60}})
	want := []ipRange{{10, 30}, {50, 60}, {100, 110}}
	if len(rs) != len(want) {
		t.Fatalf("mergeRanges = %v, want %v", rs, want)
	}
	for i := range want {
		if rs[i] != want[i] {
			t.Fatalf("mergeRanges = %v, want %v", rs, want)
		}
	}
}
