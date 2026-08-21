package exchange

import (
	"testing"
)

// TestFundingPeriodsPerDay_8h 8h 结算间隔 → 3 次/日。
func TestFundingPeriodsPerDay_8h(t *testing.T) {
	got := fundingPeriodsPerDay(8 * 3600 * 1000)
	if got != 3 {
		t.Errorf("8h interval want 3/day, got %d", got)
	}
}

// TestFundingPeriodsPerDay_4h 4h 结算间隔 → 6 次/日。
func TestFundingPeriodsPerDay_4h(t *testing.T) {
	got := fundingPeriodsPerDay(4 * 3600 * 1000)
	if got != 6 {
		t.Errorf("4h interval want 6/day, got %d", got)
	}
}

// TestFundingPeriodsPerDay_1h 1h 结算间隔 → 24 次/日。
func TestFundingPeriodsPerDay_1h(t *testing.T) {
	got := fundingPeriodsPerDay(3600 * 1000)
	if got != 24 {
		t.Errorf("1h interval want 24/day, got %d", got)
	}
}

// TestFundingPeriodsPerDay_Missing intervalMs=0 → 回退 3。
func TestFundingPeriodsPerDay_Missing(t *testing.T) {
	got := fundingPeriodsPerDay(0)
	if got != 3 {
		t.Errorf("missing interval want fallback 3, got %d", got)
	}
}

// TestFundingPeriodsPerDay_Negative intervalMs<0 → 回退 3。
func TestFundingPeriodsPerDay_Negative(t *testing.T) {
	got := fundingPeriodsPerDay(-1000)
	if got != 3 {
		t.Errorf("negative interval want fallback 3, got %d", got)
	}
}

// TestFundingPeriodsPerDay_D079Regression D-079 回归锚：
// 旧 bug 用「剩余时间」当间隔 → 结算前 55 分钟算出 26 次/天（年化 94.9%）。
// 修复后用「结算间隔」8h → 3 次/天（年化 10.95%），与当前时刻无关。
func TestFundingPeriodsPerDay_D079Regression(t *testing.T) {
	// 8h 间隔，无论当前距结算还剩多少时间，结果都是 3
	got := fundingPeriodsPerDay(8 * 3600 * 1000)
	if got != 3 {
		t.Errorf("D-079: 8h interval must give 3/day regardless of remaining time, got %d", got)
	}
	// 年化验证：0.01% × 3 × 365 × 100 = 10.95%
	annualized := annualizeFunding(0.0001, got)
	if annualized < 10.9 || annualized > 11.1 {
		t.Errorf("D-079: annualized want ~10.95%%, got %.4f%%", annualized)
	}
}

// TestAnnualizeFunding_4h 4h 币种年化 = 现值×2（D-077 修复⑪B N1 回归锚）。
func TestAnnualizeFunding_4h(t *testing.T) {
	rate := 0.0001
	oldVal := rate * 3 * 365 * 100
	newVal := annualizeFunding(rate, 6)
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
