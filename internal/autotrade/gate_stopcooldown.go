// gate_stopcooldown.go —— D-372：gate 统一止损重进冷却（closed 持仓抑制）。
// 生产实证：position 493 stop 平仓后 1 秒同组合以更差价差重进（494）——重入抑制原挂
// scanSwapArb 扫描侧独享，机会驱动路径（XASSET/MEANREVERT/detector opp）零覆盖；
// 本检查挂 gate 统一闸单点补齐全部 intent 来源（与 D-072 风控统一处理一致）。
package autotrade

import (
	"context"
	"fmt"
	"time"

	"arb/internal/combo"
)

// cooledExitKinds 冷却命中的平仓原因（reason-aware）：
// stop=基差发散止损，而价差发散同时放大入场信号→stop 落库瞬间重进是必然毒性 churn；
// manual=人工平仓后被机器人开回=违背操作意图。
// harvest（收敛收割，价差重开=合法新信号）/carry_death（复活走 entry gate 重评）/
// NULL·other（D-245 前历史行）不冷——SQL 侧排除，应用层零判断。
var cooledExitKinds = []string{"stop", "manual"}

// stopCooldownCheck D-372：同组合 stop/manual 冷却窗内平仓 → 命中。
// kind+窗口过滤已在 SQL（ListCooledExits），行自带冷却语义，应用层只剩组合键匹配；
// 多行命中取任一即拒（窗口内任一即阻断）。返回值供 gateDecision 审计 reason。
// StopReentryCooldown<=0 → 检查关闭（逃生阀，与 ReentryCooldownDays 语义对齐）。
func (g *Gate) stopCooldownCheck(ctx context.Context, intentCombo string) (hit bool, kind string, ago time.Duration, err error) {
	if g.cfg.StopReentryCooldown <= 0 {
		return false, "", 0, nil
	}
	cooled, err := g.store.ListCooledExits(ctx, cooledExitKinds, time.Now().Add(-g.cfg.StopReentryCooldown))
	if err != nil {
		return false, "", 0, fmt.Errorf("list cooled exits: %w", err)
	}
	for _, ce := range cooled {
		if ce.ClosedAt == nil {
			continue // 防御：SQL WHERE closed_at > since 已排 NULL，理论不可达
		}
		if combo.MatchOppID(ce.OpportunityID, intentCombo) {
			return true, ce.ExitKind, time.Since(*ce.ClosedAt), nil
		}
	}
	return false, "", 0, nil
}
