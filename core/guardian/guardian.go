// Package guardian issues ProxyPass bearer JWTs from Mozilla's Guardian API
// (https://vpn.mozilla.org), activates free FPN entitlements on HTTP 403,
// and parses X-Quota-* monthly traffic headers.
package guardian

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"mozillavpnlite/core/fxa"
)

const (
	Endpoint                 = "https://vpn.mozilla.org"
	DefaultMonthlyQuotaBytes = int64(5) * 1024 * 1024 * 1024 // 5 GB (5,368,709,120 bytes)
)

var (
	ErrTokenInvalid  = errors.New("OAuth токен отклонён Guardian (HTTP 401)")
	ErrNotEntitled   = errors.New("аккаунт ещё не активирован в Guardian (HTTP 403)")
	ErrQuotaExceeded = errors.New("месячная квота 5 ГБ исчерпана (HTTP 429)")
)

type QuotaUsage struct {
	Unlimited      bool      `json:"unlimited"`
	LimitBytes     int64     `json:"limit_bytes"`
	RemainingBytes int64     `json:"remaining_bytes"`
	ResetAt        time.Time `json:"reset_at"`
}

func DefaultQuotaUsage() *QuotaUsage {
	now := time.Now().UTC()
	nextMonth := time.Date(now.Year(), now.Month()+1, 1, 0, 0, 0, 0, time.UTC)
	return &QuotaUsage{
		Unlimited:      false,
		LimitBytes:     DefaultMonthlyQuotaBytes,
		RemainingBytes: DefaultMonthlyQuotaBytes,
		ResetAt:        nextMonth,
	}
}

func (u *QuotaUsage) UsedBytes() int64 {
	if u == nil || u.LimitBytes <= u.RemainingBytes {
		return 0
	}
	return u.LimitBytes - u.RemainingBytes
}

func (u *QuotaUsage) UsedPercent() float64 {
	if u == nil || u.LimitBytes <= 0 {
		return 0
	}
	return float64(u.UsedBytes()) * 100.0 / float64(u.LimitBytes)
}

type ProxyPass struct {
	RawToken  string      `json:"raw_token"`
	ExpiresAt time.Time   `json:"expires_at"`
	Usage     *QuotaUsage `json:"usage,omitempty"`
}

// QuotaEvent — одно наблюдение заголовков квоты, для диагностики в UI:
// logcat в gomobile-сборке вывод Go не показывает, поэтому события
// копятся в кольцевом буфере и отдаются в status JSON.
type QuotaEvent struct {
	At      string `json:"at"`
	Source  string `json:"source"` // "token" | "connect"
	Status  int    `json:"status"`
	Summary string `json:"summary"`
	Error   string `json:"error,omitempty"`
}

var quotaEventsMu sync.Mutex
var quotaEvents []QuotaEvent

// RecordQuotaEvent сохраняет событие в кольцевой буфер (последние 8).
func RecordQuotaEvent(source string, status int, summary string, err error) {
	errStr := ""
	if err != nil {
		errStr = err.Error()
	}
	ev := QuotaEvent{
		At:      time.Now().Format("15:04:05"),
		Source:  source,
		Status:  status,
		Summary: summary,
		Error:   errStr,
	}
	quotaEventsMu.Lock()
	quotaEvents = append(quotaEvents, ev)
	if len(quotaEvents) > 8 {
		quotaEvents = quotaEvents[len(quotaEvents)-8:]
	}
	quotaEventsMu.Unlock()
}

// RecentQuotaEvents возвращает копию кольцевого буфера событий квоты.
func RecentQuotaEvents() []QuotaEvent {
	quotaEventsMu.Lock()
	defer quotaEventsMu.Unlock()
	return append([]QuotaEvent(nil), quotaEvents...)
}

var lastTokenMu sync.Mutex
var lastTokenStatus int
var lastTokenSummary string

// FetchProxyPass requests a short-lived ProxyPass JWT from /api/v1/fpn/token.
// If Guardian responds with HTTP 403, it automatically calls /api/v1/fpn/activate and retries.
func FetchProxyPass(ctx context.Context, accessToken string) (*ProxyPass, error) {
	lastTokenMu.Lock()
	lastTokenStatus = 0
	lastTokenSummary = ""
	lastTokenMu.Unlock()

	pass, err := fetchProxyPassInternal(ctx, accessToken)

	lastTokenMu.Lock()
	st, sum := lastTokenStatus, lastTokenSummary
	lastTokenMu.Unlock()
	RecordQuotaEvent("token", st, sum, err)
	return pass, err
}

func fetchProxyPassInternal(ctx context.Context, accessToken string) (*ProxyPass, error) {
	pass, err := fetchTokenOnce(ctx, accessToken)
	if errors.Is(err, ErrNotEntitled) {
		if actErr := Activate(ctx, accessToken); actErr == nil {
			return fetchTokenOnce(ctx, accessToken)
		}
	}
	return pass, err
}

func fetchTokenOnce(ctx context.Context, accessToken string) (*ProxyPass, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, Endpoint+"/api/v1/fpn/token", nil)
	if err != nil {
		return nil, err
	}
	applyHeaders(req, accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	usage := ParseQuotaHeaders(resp.Header)
	summary := QuotaHeaderSummary(resp.Header)
	log.Printf("[quota] token fetch: status=%d %s", resp.StatusCode, summary)
	lastTokenMu.Lock()
	lastTokenStatus = resp.StatusCode
	lastTokenSummary = summary
	lastTokenMu.Unlock()
	if usage == nil {
		usage = DefaultQuotaUsage()
	}

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusUnauthorized:
		return nil, ErrTokenInvalid
	case http.StatusForbidden:
		return nil, ErrNotEntitled
	case http.StatusTooManyRequests:
		usage.RemainingBytes = 0
		return &ProxyPass{Usage: usage}, ErrQuotaExceeded
	default:
		return nil, fmt.Errorf("Guardian HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 16*1024))
	if err != nil {
		return nil, err
	}
	var tokenResp struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(body, &tokenResp); err != nil || tokenResp.Token == "" {
		return nil, errors.New("пустой токен ProxyPass в ответе Guardian")
	}

	exp := parseJWTExpiry(tokenResp.Token)
	return &ProxyPass{
		RawToken:  tokenResp.Token,
		ExpiresAt: exp,
		Usage:     usage,
	}, nil
}

// Activate enables the Firefox Private Network entitlement on the Mozilla account.
func Activate(ctx context.Context, accessToken string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, Endpoint+"/api/v1/fpn/activate", nil)
	if err != nil {
		return err
	}
	applyHeaders(req, accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Guardian activate HTTP %d", resp.StatusCode)
	}
	return nil
}

func applyHeaders(req *http.Request, accessToken string) {
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cache-Control", "max-age=0")
	req.Header.Set("User-Agent", fxa.FirefoxUserAgent)
}

// QuotaHeaderSummary renders the raw X-Quota-* header values (plus the
// parsed numbers) for diagnostics. It returns "" when the response carries
// no quota headers at all, so callers can log unconditionally without
// flooding the log on every CONNECT.
func QuotaHeaderSummary(h http.Header) string {
	if h == nil {
		return ""
	}
	rawLimit := strings.TrimSpace(h.Get("X-Quota-Limit"))
	rawRem := strings.TrimSpace(h.Get("X-Quota-Remaining"))
	rawReset := strings.TrimSpace(h.Get("X-Quota-Reset"))
	rawUnlim := strings.TrimSpace(h.Get("X-Quota-Unlimited"))
	if rawLimit == "" && rawRem == "" && rawReset == "" && rawUnlim == "" {
		return ""
	}
	parsed := "parsed=nil"
	if q := ParseQuotaHeaders(h); q != nil {
		parsed = fmt.Sprintf("parsed_limit=%d parsed_remaining=%d parsed_reset=%s unlimited=%v",
			q.LimitBytes, q.RemainingBytes, q.ResetAt.Format(time.RFC3339), q.Unlimited)
	}
	return fmt.Sprintf("limit=%q remaining=%q reset=%q unlimited=%q %s",
		rawLimit, rawRem, rawReset, rawUnlim, parsed)
}

// ParseQuotaHeaders extracts X-Quota-Limit, X-Quota-Remaining, and X-Quota-Reset headers.
func ParseQuotaHeaders(h http.Header) *QuotaUsage {
	if h == nil {
		return nil
	}
	limit, err1 := strconv.ParseInt(strings.TrimSpace(h.Get("X-Quota-Limit")), 10, 64)
	rem, err2 := strconv.ParseInt(strings.TrimSpace(h.Get("X-Quota-Remaining")), 10, 64)
	if err1 == nil && err2 == nil && limit > 0 {
		var resetAt time.Time
		rawReset := strings.TrimSpace(h.Get("X-Quota-Reset"))
		if t, err := time.Parse(time.RFC3339Nano, rawReset); err == nil {
			resetAt = t
		} else if sec, err := strconv.ParseInt(rawReset, 10, 64); err == nil && sec > 0 {
			resetAt = time.Unix(sec, 0)
		}
		return &QuotaUsage{
			Unlimited:      false,
			LimitBytes:     limit,
			RemainingBytes: rem,
			ResetAt:        resetAt,
		}
	}
	if strings.EqualFold(strings.TrimSpace(h.Get("X-Quota-Unlimited")), "true") {
		q := DefaultQuotaUsage()
		q.Unlimited = true
		return q
	}
	return nil
}

func parseJWTExpiry(rawJWT string) time.Time {
	parts := strings.Split(rawJWT, ".")
	if len(parts) != 3 {
		return time.Now().Add(6 * time.Hour)
	}
	decoded, err := base64.RawURLEncoding.DecodeString(strings.TrimRight(parts[1], "="))
	if err != nil {
		return time.Now().Add(6 * time.Hour)
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(decoded, &claims); err != nil || claims.Exp <= 0 {
		return time.Now().Add(6 * time.Hour)
	}
	return time.Unix(claims.Exp, 0)
}
