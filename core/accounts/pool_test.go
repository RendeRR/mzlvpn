package accounts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"mozillavpnlite/core/fxa"
	"mozillavpnlite/core/guardian"
)

const gb = int64(1024 * 1024 * 1024)

func usage(limit, remaining int64, reset time.Time) *guardian.QuotaUsage {
	return &guardian.QuotaUsage{LimitBytes: limit, RemainingBytes: remaining, ResetAt: reset}
}

// Серверный снимок квоты принимается как есть: локальный подсчёт
// трафика удалён (2026-11-16), сервер — единственный источник правды
// о квоте.
func TestAdoptUsageTakesServerSnapshot(t *testing.T) {
	acc := &Account{Usage: usage(50*gb, 20*gb, time.Now())}
	adoptUsageLocked(acc, usage(50*gb, 45*gb, time.Now()))
	if acc.Usage.RemainingBytes != 45*gb {
		t.Fatalf("remaining = %d, want the server snapshot 45 GB", acc.Usage.RemainingBytes)
	}
}

func TestAdoptUsageUnlimited(t *testing.T) {
	acc := &Account{Usage: usage(50*gb, 20*gb, time.Now())}
	in := usage(50*gb, 50*gb, time.Now())
	in.Unlimited = true
	adoptUsageLocked(acc, in)
	if !acc.Usage.Unlimited {
		t.Fatal("unlimited flag not adopted")
	}
	if acc.Usage.UsedPercent() != 0 {
		t.Fatal("unlimited usage must report 0% used")
	}
}

func TestAdoptUsageNilKeeps(t *testing.T) {
	acc := &Account{Usage: usage(50*gb, 20*gb, time.Now())}
	adoptUsageLocked(acc, nil)
	if acc.Usage.RemainingBytes != 20*gb {
		t.Fatal("nil snapshot must not touch usage")
	}
}

func TestHealthClassMapping(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, HealthOK},
		{genericErr("network down"), HealthUnreachable},
		{fxa.ErrReauthRequired, HealthReauth},
		{guardian.ErrQuotaExceeded, HealthQuota},
		{guardian.ErrEmptyToken, HealthNoPass},
		{guardian.ErrNotEntitled, HealthNotEntitled},
		{guardian.ErrTokenInvalid, HealthTokenInvalid},
	}
	for _, c := range cases {
		if got := HealthClass(c.err); got != c.want {
			t.Fatalf("HealthClass(%v) = %q, want %q", c.err, got, c.want)
		}
	}
}

type genericErr string

func (e genericErr) Error() string { return string(e) }

func TestRecordCheckSoftNeedsTwoFailures(t *testing.T) {
	p := &Pool{}
	acc := &Account{Status: StatusReady}
	err := genericErr("Guardian HTTP 502")

	p.recordCheckLocked(acc, HealthUnreachable, err)
	if acc.Status != StatusReady {
		t.Fatalf("after 1 soft failure status = %q, want ready", acc.Status)
	}
	if acc.FailCount != 1 || acc.LastError != "Guardian HTTP 502" {
		t.Fatalf("bookkeeping wrong: fail=%d err=%q", acc.FailCount, acc.LastError)
	}

	p.recordCheckLocked(acc, HealthUnreachable, err)
	if acc.Status != StatusUnreachable {
		t.Fatalf("after 2 soft failures status = %q, want unreachable", acc.Status)
	}
	if acc.CooldownUntil.IsZero() {
		t.Fatal("cooldown not set after condemnation")
	}

	p.recordCheckLocked(acc, HealthOK, nil)
	if acc.Status != StatusReady || acc.FailCount != 0 || acc.LastError != "" || !acc.CooldownUntil.IsZero() {
		t.Fatalf("success did not heal: status=%q fail=%d err=%q", acc.Status, acc.FailCount, acc.LastError)
	}
	if acc.LastOKAt.IsZero() {
		t.Fatal("LastOKAt not set on success")
	}
}

func TestRecordCheckHardFailuresImmediate(t *testing.T) {
	p := &Pool{}

	acc := &Account{Status: StatusReady}
	p.recordCheckLocked(acc, HealthNoPass, guardian.ErrEmptyToken)
	if acc.Status != StatusNoPass {
		t.Fatalf("no_pass: status = %q", acc.Status)
	}
	if d := time.Until(acc.CooldownUntil); d < 14*time.Minute || d > 16*time.Minute {
		t.Fatalf("no_pass cooldown ≈ %v, want ~15m", d)
	}

	acc2 := &Account{Status: StatusReady}
	p.recordCheckLocked(acc2, HealthReauth, genericErr("invalid_grant"))
	if acc2.Status != StatusReauthRequired {
		t.Fatalf("reauth: status = %q", acc2.Status)
	}

	acc3 := &Account{Status: StatusReady}
	p.recordCheckLocked(acc3, HealthQuota, guardian.ErrQuotaExceeded)
	if acc3.Status != StatusQuotaExceeded {
		t.Fatalf("quota: status = %q", acc3.Status)
	}
}

func TestCooldownLadder(t *testing.T) {
	if cooldownFor(1) != 15*time.Minute || cooldownFor(2) != 15*time.Minute ||
		cooldownFor(3) != time.Hour || cooldownFor(4) != 6*time.Hour || cooldownFor(9) != 6*time.Hour {
		t.Fatal("cooldown ladder wrong")
	}
}

func TestRecomputeKeepsHealthMarks(t *testing.T) {
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	dead := &Account{Status: StatusNoPass, Usage: usage(50*gb, 0, reset)}
	res := &Account{Status: StatusReady, Usage: usage(50*gb, 0, reset)}
	p := &Pool{ThresholdPercent: 95, Accounts: []*Account{dead, res}}
	p.recomputeLocked()
	if dead.Status != StatusNoPass {
		t.Fatalf("recompute overwrote health mark: %q", dead.Status)
	}
	if res.Status != StatusReserved {
		t.Fatalf("ready over-threshold account = %q, want reserved", res.Status)
	}
}

func TestNeedsCheck(t *testing.T) {
	now := time.Now()
	down := &Account{Status: StatusNoPass, Enabled: true, CooldownUntil: now.Add(-time.Minute)}
	if !down.NeedsCheck(now) {
		t.Fatal("expired-cooldown failure should need a check")
	}
	cooling := &Account{Status: StatusNoPass, Enabled: true, CooldownUntil: now.Add(time.Hour)}
	if cooling.NeedsCheck(now) {
		t.Fatal("account in cooldown must not be checked")
	}
	stale := &Account{Status: StatusReady, Enabled: true, LastCheckAt: now.Add(-7 * time.Hour)}
	if !stale.NeedsCheck(now) {
		t.Fatal("ready standby without a warm pass should need a check")
	}
	activeStale := &Account{Status: StatusReady, Enabled: true, Active: true, LastCheckAt: now.Add(-7 * time.Hour)}
	if !activeStale.NeedsCheck(now) {
		t.Fatal("active account without a warm pass needs a check too (app opened disconnected)")
	}
	warm := &Account{Status: StatusReady, Enabled: true, LastPass: &guardian.ProxyPass{RawToken: "x", ExpiresAt: now.Add(10 * time.Minute)}}
	if warm.NeedsCheck(now) {
		t.Fatal("ready account with a warm pass must not be re-checked")
	}
}

// Потерянный без перезахода аккаунт нельзя сделать активным: Activate
// обязан отказать до сети и не трогать флаги (полевая находка
// 2026-11-16: «В РАБОТУ» делала мёртвый профиль якобы активным).
func TestActivateRefusesReauth(t *testing.T) {
	p, err := LoadOrCreatePool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	live := &Account{ID: "live", Email: "live@x", Status: StatusReady, Enabled: true, Active: true}
	dead := &Account{ID: "dead", Email: "dead@x", Status: StatusReauthRequired, Enabled: true}
	p.Accounts = []*Account{live, dead}
	_ = p.saveLocked()

	if _, err := p.Activate(context.Background(), "dead"); !errors.Is(err, fxa.ErrReauthRequired) {
		t.Fatalf("Activate(reauth) err = %v, want ErrReauthRequired", err)
	}
	if dead.Active {
		t.Fatal("reauth account got flagged active")
	}
	if !live.Active {
		t.Fatal("live account lost the active flag")
	}
}

// Вердикт reauth выводит аккаунт из ротации: карточка не должна
// утверждать «в ротации» о потерянном аккаунте.
func TestRecordCheckReauthLeavesRotation(t *testing.T) {
	p := &Pool{}
	acc := &Account{Status: StatusReady, Enabled: true}
	p.recordCheckLocked(acc, HealthReauth, fxa.ErrReauthRequired)
	if acc.Status != StatusReauthRequired || acc.Enabled {
		t.Fatalf("reauth verdict: status=%q enabled=%v", acc.Status, acc.Enabled)
	}
}

// Persistence hardening (2026-11-16): a corrupt pool file must fall
// back to the .bak copy, never silently become an empty pool; with
// no valid copy anywhere, loading must fail loudly.
func TestLoadFallsBackToBackup(t *testing.T) {
	dir := t.TempDir()
	fp := filepath.Join(dir, "accounts_pool.json")
	if err := os.WriteFile(fp, []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fp+".bak", []byte(`{"threshold_percent": 90, "accounts": []}`), 0600); err != nil {
		t.Fatal(err)
	}
	p, err := LoadOrCreatePool(dir)
	if err != nil {
		t.Fatalf("load with valid backup: %v", err)
	}
	if p.ThresholdPercent != 90 {
		t.Fatalf("threshold = %d, want 90 (from backup)", p.ThresholdPercent)
	}
}

func TestLoadCorruptWithoutBackupFails(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "accounts_pool.json"), []byte("{corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreatePool(dir); err == nil {
		t.Fatal("corrupt pool without backup must return an error, not an empty pool")
	}
}

func TestSaveKeepsBackupAndValidMain(t *testing.T) {
	dir := t.TempDir()
	p, err := LoadOrCreatePool(dir)
	if err != nil {
		t.Fatal(err)
	}
	save := func() {
		p.mu.Lock()
		defer p.mu.Unlock()
		if err := p.saveLocked(); err != nil {
			t.Fatal(err)
		}
	}
	save()
	fp := filepath.Join(dir, "accounts_pool.json")
	if _, err := os.Stat(fp + ".bak"); !os.IsNotExist(err) {
		t.Fatal(".bak must not exist after the very first save")
	}
	p.ThresholdPercent = 90
	save()
	bak, err := os.ReadFile(fp + ".bak")
	if err != nil {
		t.Fatalf("second save must keep a backup: %v", err)
	}
	var probe Pool
	if err := json.Unmarshal(bak, &probe); err != nil {
		t.Fatalf("backup is not valid JSON: %v", err)
	}
	if probe.ThresholdPercent != 95 {
		t.Fatalf("backup threshold = %d, want the previous 95", probe.ThresholdPercent)
	}
	p2, err := LoadOrCreatePool(dir)
	if err != nil {
		t.Fatal(err)
	}
	if p2.ThresholdPercent != 90 {
		t.Fatalf("reloaded threshold = %d, want 90", p2.ThresholdPercent)
	}
}

// Регрессия полевой находки 2026-11-16: obtainPass обязан принимать
// безлимитный снимок квоты. Страж «!Unlimited» времён локального
// подсчёта молча сохранял старую ограниченную квоту навсегда.
func TestObtainPassAdoptsUnlimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Quota-Limit", "53687091200")
		w.Header().Set("X-Quota-Remaining", "53687091200")
		w.Header().Set("X-Quota-Unlimited", "true")
		exp := time.Now().Add(10 * time.Minute).Unix()
		payload := base64.RawURLEncoding.EncodeToString([]byte(`{"exp":` + strconv.FormatInt(exp, 10) + `}`))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"x.` + payload + `.y"}`))
	}))
	defer srv.Close()
	old := guardian.Endpoint
	guardian.Endpoint = srv.URL
	defer func() { guardian.Endpoint = old }()

	p, err := LoadOrCreatePool(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	acc := &Account{
		ID: "t1", Email: "t@example", Status: StatusReady, Enabled: true,
		Usage: usage(50*gb, 20*gb, time.Now()),
	}
	acc.Tokens.AccessToken = "at"
	acc.Tokens.ExpiresAt = time.Now().Add(time.Hour)
	p.Accounts = []*Account{acc}

	pass, err := p.obtainPass(context.Background(), acc)
	if err != nil {
		t.Fatal(err)
	}
	if pass == nil || pass.RawToken == "" {
		t.Fatal("no pass returned")
	}
	if acc.Usage == nil || !acc.Usage.Unlimited {
		t.Fatalf("unlimited snapshot not adopted: %+v", acc.Usage)
	}
}
