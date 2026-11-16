package accounts

import (
	"testing"
	"time"

	"mozillavpnlite/core/guardian"
)

const gb = int64(1024 * 1024 * 1024)

func usage(limit, remaining int64, reset time.Time) *guardian.QuotaUsage {
	return &guardian.QuotaUsage{LimitBytes: limit, RemainingBytes: remaining, ResetAt: reset}
}

// Свежий ответ токен-эндпоинта (remaining = полный лимит) НЕ должен
// стирать локально насчитанный расход — это был баг «трафик только за
// текущую сессию».
func TestMergeKeepsLocalUsageOnFreshGrant(t *testing.T) {
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	acc := &Account{Usage: usage(50*gb, 47*gb, reset)} // потрачено 3 ГБ
	mergeUsageLocked(acc, usage(50*gb, 50*gb, reset))  // сервер: снова полный
	if got := acc.Usage.UsedBytes(); got != 3*gb {
		t.Fatalf("used = %d, want %d", got, 3*gb)
	}
	if acc.Usage.RemainingBytes != 47*gb {
		t.Fatalf("remaining = %d, want %d", acc.Usage.RemainingBytes, 47*gb)
	}
}

// Сервер сообщает больший расход (например, трафик с другого устройства) —
// берём большее значение.
func TestMergeAdoptsLargerServerUsage(t *testing.T) {
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	acc := &Account{Usage: usage(50*gb, 49*gb, reset)} // локально 1 ГБ
	mergeUsageLocked(acc, usage(50*gb, 45*gb, reset))  // сервер: потрачено 5 ГБ
	if got := acc.Usage.UsedBytes(); got != 5*gb {
		t.Fatalf("used = %d, want %d", got, 5*gb)
	}
}

// Сменился лимит (был синтетический дефолт 5 ГБ, сервер дал 50 ГБ):
// потраченные байты сохраняются, остаток пересчитывается от нового лимита.
func TestMergeSurvivesLimitChange(t *testing.T) {
	reset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	acc := &Account{Usage: usage(5*gb, 4*gb, reset)} // потрачен 1 ГБ
	mergeUsageLocked(acc, usage(50*gb, 50*gb, reset))
	if got := acc.Usage.UsedBytes(); got != 1*gb {
		t.Fatalf("used = %d, want %d", got, 1*gb)
	}
	if acc.Usage.LimitBytes != 50*gb || acc.Usage.RemainingBytes != 49*gb {
		t.Fatalf("usage = %+v", acc.Usage)
	}
}

// Новый период квоты (ResetAt позже сохранённого) — счёт начинается заново.
func TestMergeNewPeriodResets(t *testing.T) {
	oldReset := time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)
	newReset := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	acc := &Account{Usage: usage(50*gb, 1*gb, oldReset)} // почти всё потрачено
	mergeUsageLocked(acc, usage(50*gb, 50*gb, newReset))
	if got := acc.Usage.UsedBytes(); got != 0 {
		t.Fatalf("used = %d, want 0 in a new period", got)
	}
	if !acc.Usage.ResetAt.Equal(newReset) {
		t.Fatalf("reset_at = %v, want %v", acc.Usage.ResetAt, newReset)
	}
}

// Нет сохранённого usage — принимается серверный как есть.
func TestMergeIntoEmpty(t *testing.T) {
	acc := &Account{}
	mergeUsageLocked(acc, usage(50*gb, 40*gb, time.Now()))
	if acc.Usage == nil || acc.Usage.RemainingBytes != 40*gb {
		t.Fatalf("usage = %+v", acc.Usage)
	}
}

// --- Health bookkeeping (2026-11-16) ---

func TestHealthClassMapping(t *testing.T) {
	cases := []struct {
		err  error
		want string
	}{
		{nil, HealthOK},
		{genericErr("network down"), HealthUnreachable},
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
		t.Fatal("stale ready standby should need a check")
	}
	activeStale := &Account{Status: StatusReady, Enabled: true, Active: true, LastCheckAt: now.Add(-7 * time.Hour)}
	if activeStale.NeedsCheck(now) {
		t.Fatal("active account is maintained by live paths, not the sweep")
	}
}
