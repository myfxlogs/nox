package main

// diff_basis.go —— 基差轴纯函数件（瞬态 bps+交割年化；原 diff.go 拆分，零行为变）。

import (
	"math"
	"time"
)

// basisBps D-354 S1：基差=(mark/index−1)×1e4，round 1 位；mark≤0 或 index≤0 → nil
// （fail-closed，不臆造；okx 行无 index 面→NULL 诚实缺）。
func basisBps(mark, index float64) *float64 {
	if mark <= 0 || index <= 0 {
		return nil
	}
	v := math.Round((mark/index-1)*1e4*10) / 10
	return &v
}

// basisAnn 交割基差年化 %=(mark/index−1)×365/days_to_expiry×100（到期收敛速率口径）。
// fail-closed：mark/index≤0、expiry 零值或已过/当日（days<0.5）→nil，禁猜到期日。
func basisAnn(mark, index float64, expiry time.Time, now time.Time) *float64 {
	if mark <= 0 || index <= 0 || expiry.IsZero() {
		return nil
	}
	days := expiry.Sub(now).Hours() / 24
	if days < 0.5 {
		return nil
	}
	v := math.Round((mark/index-1)*365/days*100*10) / 10
	return &v
}
