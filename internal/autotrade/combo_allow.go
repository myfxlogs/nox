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
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("combo in denylist: %s %s×%s", intent.Canonical, intent.VenueBuy, intent.VenueSell)}
	}

	if !autoAllow {
		// D-159 原始模式：白名单空=fail-closed，白名单非空=精确匹配
		if len(allow) == 0 {
			return GateVerdict{Pass: false, Reason: "no swaparb combo allowlist configured"}
		}
		if exec.MatchComboAllow(allow, intent.Canonical, intent.VenueBuy, intent.VenueSell) == nil {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("combo not in allowlist: %s %s×%s", intent.Canonical, intent.VenueBuy, intent.VenueSell)}
		}
	}
	// D-168 autoAllow=true：白名单不拦截（空或非空都放行），由门禁栈兜底

	if maxActive <= 0 {
		maxActive = 1
	}
	intentCombo := combo.Key(intent.Strategy, intent.Canonical, intent.VenueBuy, intent.VenueSell)
	activeCount := 0
	for _, p := range activePositions {
		if p.OpportunityID == "" {
			continue
		}
		// D-181 S2：probe 仓不计 MAX_ACTIVE_COMBOS（note LIKE 'PROBE%'）。
		if strings.HasPrefix(p.Note, "PROBE") {
			continue
		}
		if combo.MatchOppID(p.OpportunityID, intentCombo) {
			activeCount++
		}
	}
	if activeCount >= maxActive {
		return GateVerdict{Pass: false, Reason: fmt.Sprintf("max active combos reached (%d >= %d)", activeCount, maxActive)}
	}
	// D-179 S3：全局额度检查——真实 Σ 活跃 SWAPARB notional_usd 替代旧估算（仓数×新组合名义）。
	// notional_usd 是双腿口径（positions.notional_usd=全部腿名义合计）；intent.NotionalUSD 是单腿口径。
	// nil 计 0（回填缺失/新仓初始 NULL 窗口）；纯函数不打日志，NULL warn 在调用点 gate.go Submit 预扫。
	if maxGrossExposure > 0 {
		activeGross := 0.0
		for _, p := range activePositions {
			if p.OpportunityID == "" || !strings.HasPrefix(p.OpportunityID, "SWAPARB:") {
				continue
			}
			if p.NotionalUSD != nil {
				activeGross += *p.NotionalUSD
			}
		}
		remaining := maxGrossExposure - activeGross // 双腿空间
		if remaining <= 0 {
			return GateVerdict{Pass: false, Reason: fmt.Sprintf("gross exposure exhausted: active $%.2f >= $%.2f", activeGross, maxGrossExposure)}
		}
		// v2-F 全链双腿换算：remaining（双腿）vs intent.NotionalUSD×2（双腿）比较
		if remaining < intent.NotionalUSD*2 {
			// 折分：AdjustedNotional=remaining/2（单腿目标名义），保持单腿语义（D-168 ratio 链不动）
			adjusted := remaining / 2
			return GateVerdict{Pass: true, AdjustedNotional: adjusted, Reason: fmt.Sprintf("gross adjusted: $%.2f → $%.2f (remaining within $%.2f cap)", intent.NotionalUSD, adjusted, maxGrossExposure)}
		}
	}
	return GateVerdict{Pass: true}
}
