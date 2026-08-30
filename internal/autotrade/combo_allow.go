package autotrade

import (
	"fmt"

	"arb/internal/combo"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// comboAllowVerdict D-159 W1a R1：精确组合门禁判定（纯函数，无 IO）。
// 调用点：gate.Submit，在 gateDecision 通过后、Execute 前。
// 前置条件：state=RUNNING（gateDecision 已过滤非 RUNNING）。
// 判定顺序：
//  1. SwapArbAllowCombos 空 → fail-closed "no swaparb combo allowlist configured"
//  2. intent 不在 allowlist → "combo not in allowlist"
//  3. 该组合 active 数 ≥ MaxActiveCombos → "max active combos reached"
//
// MaxActiveCombos ≤0 视为 1（D-138 单组合口径默认）。
//
// ComboAllowSpec 定义在 exec 包（最底层无依赖，dashboard/autotrade 复用）。
func comboAllowVerdict(allow []exec.ComboAllowSpec, maxActive int, intent TradeIntent, activePositions []pgstore.Position) GateVerdict {
	if len(allow) == 0 {
		return GateVerdict{false, "no swaparb combo allowlist configured"}
	}
	spec := exec.MatchComboAllow(allow, intent.Canonical, intent.VenueBuy, intent.VenueSell)
	if spec == nil {
		return GateVerdict{false, fmt.Sprintf("combo not in allowlist: %s %s×%s", intent.Canonical, intent.VenueBuy, intent.VenueSell)}
	}
	if maxActive <= 0 {
		maxActive = 1
	}
	intentCombo := combo.Key(intent.Strategy, intent.Canonical, intent.VenueBuy, intent.VenueSell)
	activeCount := 0
	for _, p := range activePositions {
		if p.OpportunityID == "" {
			continue
		}
		if combo.MatchOppID(p.OpportunityID, intentCombo) {
			activeCount++
		}
	}
	if activeCount >= maxActive {
		return GateVerdict{false, fmt.Sprintf("max active combos reached (%d >= %d)", activeCount, maxActive)}
	}
	return GateVerdict{Pass: true}
}
