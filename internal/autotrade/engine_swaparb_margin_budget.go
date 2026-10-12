package autotrade

// D-142 ④：组合保证金预算观察版告警（只告警不拦截）+ 杠杆不对称提示。
// 在 trySwapArb gate.Submit 前调用，best-effort 不影响开仓流程。
// 保证金 = notional × 1/leverage（账户级回落，品种级 InitMarginRate 网关不返回见 ③）。
// 无限杠杆（2e9 哨兵）→ 保证金≈0，不告警。

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
	"math"
	"time"
)

// aliasOrKey D-144 ③：venueKey → alias（有别名显示别名，无则回退 venueKey）。
// 从 store 加载 venue→alias 映射（best-effort，查询失败 → 回退 venueKey）。
// 告警消息用——observeMarginBudget 已有 1h 去重，加载频率低可接受。
func (e *Engine) aliasOrKey(venueKey string) string {
	if e.store == nil {
		return venueKey
	}
	aliasMap, err := e.store.ListAccountAliases(context.Background())
	if err != nil {
		return venueKey
	}
	venue := keydomain.BareOf(venueKey)
	if a, ok := aliasMap[venue]; ok && a != "" {
		return a
	}
	return venueKey
}

// marginAlertDedupInterval D-144 ②：per-key 去重窗口（1h，同 mt-health critRealertInterval 模式）。
const marginAlertDedupInterval = time.Hour

// shouldMarginAlert D-144 ②：per-key 去重——key=canonical:vl:vs:type，1h 窗口内不重发。
// 线程安全（marginAlertMu 保护）。返回 true=允许告警并记录时间，false=窗口内跳过。
// marginAlertSent nil（测试直接构造 Engine）→ 初始化为空 map（防御性，避免 panic）。
func (e *Engine) shouldMarginAlert(key string) bool {
	e.marginAlertMu.Lock()
	defer e.marginAlertMu.Unlock()
	if e.marginAlertSent == nil {
		e.marginAlertSent = make(map[string]time.Time)
	}
	if last, ok := e.marginAlertSent[key]; ok && time.Since(last) < marginAlertDedupInterval {
		return false
	}
	dedupCapHalf(e.marginAlertSent) // D-508 S5：无界去重集封顶
	e.marginAlertSent[key] = time.Now()
	return true
}

// observeMarginBudget D-142 ④：观察版告警（只告警不拦截）。
// ① 组合保证金预算：双腿合计保证金 / min(equity) > SwapArbMarginBudgetPct → warn 告警。
// ② 杠杆不对称：两腿 leverage 差异 > 2 倍（且均非无限杠杆）→ info 提示。
// D-144 ②：per-key 去重（canonical:vl:vs:type，1h 窗口）——防告警洪水。
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
			key := fmt.Sprintf("%s:%s:%s:asymmetry", canonical, venueLong, venueShort)
			if e.shouldMarginAlert(key) {
				e.AlertSink("info", fmt.Sprintf(
					"套息组合 %s：双腿杠杆不对称 %s=%.0f× vs %s=%.0f×（差 %.1f 倍，保证金分配不均）",
					canonical, e.aliasOrKey(venueLong), levLong, e.aliasOrKey(venueShort), levShort, ratio))
			}
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
		key := fmt.Sprintf("%s:%s:%s:budget", canonical, venueLong, venueShort)
		if e.shouldMarginAlert(key) {
			e.AlertSink("warn", fmt.Sprintf(
				"套息组合 %s：保证金占比 %.1f%% 超阈值 %.0f%%（保证金 $%.0f = 多腿 $%.0f + 空腿 $%.0f，较小净值 $%.0f）",
				canonical, budgetPct*100, e.cfg.SwapArbMarginBudgetPct*100,
				totalMargin, marginLong, marginShort, minEq))
		}
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
