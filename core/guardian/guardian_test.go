package guardian

import (
	"net/http"
	"testing"
)

// The promo case that shipped broken (found 2026-11-16): the server
// sends limit, remaining AND X-Quota-Unlimited together, and the
// flag must survive parsing.
func TestParseQuotaHeadersUnlimitedWithNumbers(t *testing.T) {
	h := http.Header{}
	h.Set("X-Quota-Limit", "53687091200")
	h.Set("X-Quota-Remaining", "53687091200")
	h.Set("X-Quota-Reset", "2026-12-01T00:00:00Z")
	h.Set("X-Quota-Unlimited", "true")
	q := ParseQuotaHeaders(h)
	if q == nil {
		t.Fatal("expected a quota snapshot")
	}
	if !q.Unlimited {
		t.Fatal("unlimited flag dropped when numeric headers are present")
	}
	if q.LimitBytes != 53687091200 || q.RemainingBytes != 53687091200 {
		t.Fatalf("numbers mangled: limit=%d remaining=%d", q.LimitBytes, q.RemainingBytes)
	}
}

func TestParseQuotaHeadersCapped(t *testing.T) {
	h := http.Header{}
	h.Set("X-Quota-Limit", "53687091200")
	h.Set("X-Quota-Remaining", "1000")
	q := ParseQuotaHeaders(h)
	if q == nil || q.Unlimited {
		t.Fatalf("plain capped headers must not be unlimited: %+v", q)
	}
}

func TestParseQuotaHeadersUnlimitedOnly(t *testing.T) {
	h := http.Header{}
	h.Set("X-Quota-Unlimited", "true")
	q := ParseQuotaHeaders(h)
	if q == nil || !q.Unlimited {
		t.Fatalf("unlimited-only headers: %+v", q)
	}
}

func TestParseQuotaHeadersNone(t *testing.T) {
	if q := ParseQuotaHeaders(http.Header{}); q != nil {
		t.Fatalf("no quota headers must give nil, got %+v", q)
	}
}
