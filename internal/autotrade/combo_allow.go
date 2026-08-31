package autotrade

import (
	"fmt"
	"strings"

	"arb/internal/combo"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// comboAllowVerdict D-159 W1a R1 + D-168 自动准入：SWAPARB 组合门禁判定（纯函数，无 IO）。
// 调用点：gate.Submit，在 gateDecision 通过后、Execute 前。
// 前置条件：state=RUNNING（gateDecision 已过滤非 RUNNING）。
//
// D-168 自动准入模式（autoAllow=true）：
//   - 白名单空 → 自动放行所有通过门禁栈的组合（不再 fail-closed）
//   - 白名单非空 → 白名单内放行 + 白名单外也放行（白名单仅作为额度配置载体）
//   - 黑名单（deny）优先：在 deny 列表中的组合一律拒绝
//   - 全局额度（maxGrossExposure>0）：所有活跃组合名义合计 + 新组合名义 ≤ 上限
//
// D-159 原始模式（autoAllow=false，向后兼容）：
//  1. SwapArbAllowCombos 空 → fail-closed "no swaparb combo allowlist configured"
//  2. intent 不在 allowlist → "combo not in allowlist"
//  3. 该组合 active 数 ≥ MaxActiveCombos → "max active combos reached"
//
// MaxActiveCombos ≤0 视为 1（D-138 单组合口径默认）。
func comboAllowVerdict(allow []exec.ComboAllowSpec, deny []exec.ComboAllowSpec, autoAllow bool, maxActive int, maxGrossExposure float64, intent TradeIntent, activePositions []pgstore.Position) GateVerdict {
	// D-168 黑名单优先：在 deny 列表中的组合一律拒绝
	if exec.MatchComboAllow(deny, intent.Canonical, intent.VenueBuy, intent.VenueSell) != nil {
		return GateVerdict{false, fmt.Sprintf("combo in denylist: %s %s×%s", intent.Canonical, intent.VenueBuy, intent.VenueSell)}
	}

	if !autoAllow {
		// D-159 原始模式：白名单空=fail-closed，白名单非空=精确匹配
		if len(allow) == 0 {
			return GateVerdict{false, "no swaparb combo allowlist configured"}
		}
		if exec.MatchComboAllow(allow, intent.Canonical, intent.VenueBuy, intent.VenueSell) == nil {
			return GateVerdict{false, fmt.Sprintf("combo not in allowlist: %s %s×%s", intent.Canonical, intent.VenueBuy, intent.VenueSell)}
		}
	}
	// D-168 autoAllow=true：白名单不拦截（空或非空都放行），由门禁栈兜底

	if maxActive <= 0 {
		maxActive = 1
	}
	intentCombo := combo.Key(intent.Strategy, intent.Canonical, intent.VenueBuy, intent.VenueSell)
	activeCount := 0
	activeSwapArbCount := 0
	for _, p := range activePositions {
		if p.OpportunityID == "" {
			continue
		}
		if combo.MatchOppID(p.OpportunityID, intentCombo) {
			activeCount++
		}
		// D-168 全局额度：统计所有活跃 SWAPARB 组合数（Position 无 NotionalUSD 字段，
		// 用活跃组合数 × intent.NotionalUSD 估算全局名义合计）
		if strings.HasPrefix(p.OpportunityID, "SWAPARB:") {
			activeSwapArbCount++
		}
	}
	if activeCount >= maxActive {
		return GateVerdict{false, fmt.Sprintf("max active combos reached (%d >= %d)", activeCount, maxActive)}
	}
	// D-168 全局额度检查（>0 才校验）：估算活跃名义 = 活跃 SWAPARB 组合数 × 新组合名义
	if maxGrossExposure > 0 {
		estimatedGross := float64(activeSwapArbCount) * intent.NotionalUSD
		if estimatedGross+intent.NotionalUSD > maxGrossExposure {
			return GateVerdict{false, fmt.Sprintf("gross exposure exceeded: estimated $%.2f + new $%.2f > $%.2f", estimatedGross, intent.NotionalUSD, maxGrossExposure)}
		}
	}
	return GateVerdict{Pass: true}
}
