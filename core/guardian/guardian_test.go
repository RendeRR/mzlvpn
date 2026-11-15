package guardian

import (
	"errors"
	"net/http"
	"strings"
	"testing"
)

func TestQuotaHeaderSummaryEmpty(t *testing.T) {
	if s := QuotaHeaderSummary(http.Header{}); s != "" {
		t.Fatalf("summary = %q, want empty", s)
	}
	if s := QuotaHeaderSummary(nil); s != "" {
		t.Fatalf("summary = %q, want empty for nil header", s)
	}
}

func TestQuotaHeaderSummaryValues(t *testing.T) {
	h := http.Header{}
	h.Set("X-Quota-Limit", "53687091200")
	h.Set("X-Quota-Remaining", "53687091200")
	h.Set("X-Quota-Reset", "2026-12-01T00:00:00Z")
	s := QuotaHeaderSummary(h)
	for _, want := range []string{`limit="53687091200"`, `remaining="53687091200"`, "parsed_limit=53687091200", "parsed_remaining=53687091200"} {
		if !strings.Contains(s, want) {
			t.Fatalf("summary %q missing %q", s, want)
		}
	}
}

func TestQuotaEventRing(t *testing.T) {
	quotaEventsMu.Lock()
	quotaEvents = nil
	quotaEventsMu.Unlock()

	for i := 0; i < 10; i++ {
		RecordQuotaEvent("token", 200, "x", nil)
	}
	RecordQuotaEvent("token", 0, "", errors.New("boom"))

	evs := RecentQuotaEvents()
	if len(evs) != 8 {
		t.Fatalf("events = %d, want ring capped at 8", len(evs))
	}
	last := evs[len(evs)-1]
	if last.Error != "boom" || last.Status != 0 {
		t.Fatalf("last event = %+v", last)
	}
}
