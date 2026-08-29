package autotrade

// D-142 ④：组合保证金预算观察版告警（只告警不拦截）+ 杠杆不对称提示。
// 在 trySwapArb gate.Submit 前调用，best-effort 不影响开仓流程。
// 保证金 = notional × 1/leverage（账户级回落，品种级 InitMarginRate 网关不返回见 ③）。
// 无限杠杆（2e9 哨兵）→ 保证金≈0，不告警。

import (
	"fmt"
	"math"
)

// observeMarginBudget D-142 ④：观察版告警（只告警不拦截）。
// ① 组合保证金预算：双腿合计保证金 / min(equity) > SwapArbMarginBudgetPct → warn 告警。
// ② 杠杆不对称：两腿 leverage 差异 > 2 倍（且均非无限杠杆）→ info 提示。
// AlertSink nil 或配置 0 → no-op。best-effort：查询失败静默跳过。
func (e *Engine) observeMarginBudget(canonical, venueLong, venueShort string, notional float64) {
	if e.AlertSink == nil {
		return
	}
	levLong, okLong := e.service.AccountLeverage(venueLong)
	levShort, okShort := e.service.AccountLeverage(venueShort)

	// ① 杠杆不对称提示（两腿 leverage 均已知且非无限杠杆）
	if okLong && okShort && isFiniteLeverage(levLong) && isFiniteLeverage(levShort) {
		if ratio := leverageAsymmetryRatio(levLong, levShort); ratio > 2.0 {
			e.AlertSink("info", fmt.Sprintf(
				"SWAPARB %s: 杠杆不对称 %s=%.0f vs %s=%.0f (%.1fx 差异，保证金分配不均)",
				canonical, venueLong, levLong, venueShort, levShort, ratio))
		}
	}

	// ② 组合保证金预算告警
	if e.cfg.SwapArbMarginBudgetPct <= 0 {
		return
	}
	eqLong, _, onlineLong := e.service.AccountEquityUSD(venueLong)
	eqShort, _, onlineShort := e.service.AccountEquityUSD(venueShort)
	if !onlineLong || !onlineShort || eqLong <= 0 || eqShort <= 0 {
		return
	}
	minEq := eqLong
	if eqShort < minEq {
		minEq = eqShort
	}
	marginLong := legMargin(notional, levLong, okLong)
	marginShort := legMargin(notional, levShort, okShort)
	totalMargin := marginLong + marginShort
	if totalMargin <= 0 {
		return // 双腿均无限杠杆 → 保证金≈0，不告警
	}
	budgetPct := totalMargin / minEq
	if budgetPct > e.cfg.SwapArbMarginBudgetPct {
		e.AlertSink("warn", fmt.Sprintf(
			"SWAPARB %s: 组合保证金预算 %.1f%% > 阈值 %.0f%% (保证金 $%.0f = L$%.0f+S$%.0f, minEq $%.0f)",
			canonical, budgetPct*100, e.cfg.SwapArbMarginBudgetPct*100,
			totalMargin, marginLong, marginShort, minEq))
	}
}

// legMargin 单腿保证金 = notional × 1/leverage（账户级回落）。
// leverage 未知或无限杠杆（2e9 哨兵）→ 0（不贡献保证金）。
func legMargin(notional, leverage float64, ok bool) float64 {
	if !ok || !isFiniteLeverage(leverage) || leverage <= 0 {
		return 0
	}
	return notional / leverage
}

// isFiniteLeverage 判断是否为有限杠杆（非 Exness 2e9 无限杠杆哨兵）。
func isFiniteLeverage(lev float64) bool {
	return lev > 0 && lev < 1e9
}

// leverageAsymmetryRatio 两腿杠杆差异倍数 = max/min。
// 任一为 0 → 返回 1（无差异，调用方已过滤）。
func leverageAsymmetryRatio(levA, levB float64) float64 {
	if levA <= 0 || levB <= 0 {
		return 1
	}
	return math.Max(levA, levB) / math.Min(levA, levB)
}
