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
