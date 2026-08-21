package exchange

import (
	"testing"
	"time"
)

// TestFundingPeriodsPerDay_4h 4h 结算周期 → 6 次/日（D-077 修复⑪B N1）。
func TestFundingPeriodsPerDay_4h(t *testing.T) {
	// nextFundingTime = now + 4h
	nextFunding := float64(time.Now().UnixMilli()) + 4*3600*1000
	got := fundingPeriodsPerDay(nextFunding)
	if got != 6 {
		t.Errorf("4h period want 6/day, got %d", got)
	}
}

// TestFundingPeriodsPerDay_8h 8h 结算周期 → 3 次/日（现行为）。
func TestFundingPeriodsPerDay_8h(t *testing.T) {
	nextFunding := float64(time.Now().UnixMilli()) + 8*3600*1000
	got := fundingPeriodsPerDay(nextFunding)
	if got != 3 {
		t.Errorf("8h period want 3/day, got %d", got)
	}
}

// TestFundingPeriodsPerDay_1h 1h 结算周期 → 24 次/日。
func TestFundingPeriodsPerDay_1h(t *testing.T) {
	nextFunding := float64(time.Now().UnixMilli()) + 3600*1000
	got := fundingPeriodsPerDay(nextFunding)
	if got != 24 {
		t.Errorf("1h period want 24/day, got %d", got)
	}
}

// TestFundingPeriodsPerDay_Missing nextFundingTime=0 → 回退 3。
func TestFundingPeriodsPerDay_Missing(t *testing.T) {
	got := fundingPeriodsPerDay(0)
	if got != 3 {
		t.Errorf("missing nextFundingTime want fallback 3, got %d", got)
	}
}

// TestFundingPeriodsPerDay_Expired nextFundingTime 已过期 → 回退 3。
func TestFundingPeriodsPerDay_Expired(t *testing.T) {
	expired := float64(time.Now().UnixMilli()) - 1000 // 1s 前
	got := fundingPeriodsPerDay(expired)
	if got != 3 {
		t.Errorf("expired nextFundingTime want fallback 3, got %d", got)
	}
}

// TestAnnualizeFunding_4h 4h 币种年化 = 现值×2（D-077 修复⑪B N1 回归锚）。
// 旧公式 f×3×365×100；4h 实际 f×6×365×100 = 2× 旧值。
func TestAnnualizeFunding_4h(t *testing.T) {
	rate := 0.0001                      // 0.01%
	oldVal := rate * 3 * 365 * 100      // 旧公式（8h×3）
	newVal := annualizeFunding(rate, 6) // 4h×6
	if newVal != 2*oldVal {
		t.Errorf("4h annualized want 2× old (%.4f), got %.4f", 2*oldVal, newVal)
	}
}

// TestAnnualizeFunding_8h 8h 币种年化 = 现值（不变）。
func TestAnnualizeFunding_8h(t *testing.T) {
	rate := 0.0001
	oldVal := rate * 3 * 365 * 100
	newVal := annualizeFunding(rate, 3)
	if newVal != oldVal {
		t.Errorf("8h annualized want same as old (%.4f), got %.4f", oldVal, newVal)
	}
}
