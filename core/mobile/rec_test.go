package mobile

import (
	"testing"

	"mozillavpnlite/core/serverlist"
)

// "Recommended" must resolve to Mozilla's anycast endpoint — not to
// the fastest country and not to a hardcoded Germany — while
// "fastest" and an explicit country keep their meaning.
func TestCurrentLocationRecommendedUsesAnycast(t *testing.T) {
	mu.Lock()
	defer mu.Unlock()
	oldSel, oldRec, oldLocs := selectedCode, recLocation, locations
	defer func() { selectedCode, recLocation, locations = oldSel, oldRec, oldLocs }()

	locations = []serverlist.Location{
		{Code: "NO", Name: "Norway", ServerHost: "osl65.m1.fastly-masque.net", ServerPort: 2499, PingMS: 81},
		{Code: "DE", Name: "Germany", ServerHost: "muc139.m1.fastly-masque.net", ServerPort: 2499, PingMS: 114},
	}
	recLocation = serverlist.DefaultRecommended()

	selectedCode = "REC"
	if got := currentLocationLocked(); got.Code != "REC" || got.ServerHost != "p.m1.fastly-masque.net" {
		t.Fatalf("REC resolved to %s (%s), want the anycast endpoint", got.Code, got.ServerHost)
	}

	selectedCode = "fastest"
	if got := currentLocationLocked(); got.Code != "NO" {
		t.Fatalf("fastest resolved to %s, want NO (locations[0])", got.Code)
	}

	selectedCode = "DE"
	if got := currentLocationLocked(); got.Code != "DE" {
		t.Fatalf("explicit DE resolved to %s", got.Code)
	}
}
