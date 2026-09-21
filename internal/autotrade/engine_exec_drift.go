// engine_exec_drift.go —— D-378a S2：XASSET 执行质量闸（exec_drift）+ harvestCheck 整组移入
// （engine_position.go 300 行门禁；harvest 为纯搬移零行为变更，XASSET 退出链出口同文件族）。
package autotrade

import (
	"context"
	"fmt"
	"math"
	"strings"

	"arb/internal/store/pgstore"
)

// execDriftCheck D-378a S2：执行质量闸——XASSET 退出顺序 harvest 之后、basisStop 之前插入。
// fill 口径随时可重算（EntrySpreadBpsFromLegs 现成），漂移=fill−quote 锚（S1 起锚为纯报价）；
// |drift|>thresh → 双向都杀（正漂移=少收、负漂移=多付，都是执行质量破产）。
// thresh=max(cfg.XAssetExecDriftBps, entry_noise_bps)（noise 自适应抬阈：6.9 类噪声仓更保守；
// 实证 494 漂移≈0、495≈5~7、493≈14.3——阈 8 杀 493 型放 494/495 型）。
// 兼容性：存量仓旧锚本就是 fill 值 → drift≈0 → 永不误杀（天然向后兼容，勿「修复」）；
// 无锚（NULL）/腿未 FILLED（ok=false）→ 不判。
func (e *Engine) execDriftCheck(ctx context.Context, p *pgstore.Position) (bool, string) {
	if p.ExpectedEntrySpreadBps == nil {
		return false, ""
	}
	fillBps, ok, err := e.store.EntrySpreadBpsFromLegs(ctx, p.ID)
	if err != nil || !ok {
		return false, ""
	}
	drift := fillBps - *p.ExpectedEntrySpreadBps
	thresh := e.cfg.XAssetExecDriftBps
	if p.EntryNoiseBps != nil && *p.EntryNoiseBps > thresh {
		thresh = *p.EntryNoiseBps
	}
	if math.Abs(drift) > thresh {
		return true, fmt.Sprintf("exec_drift fill %.2fbps vs quote anchor %.2fbps", fillBps, *p.ExpectedEntrySpreadBps)
	}
	return false, ""
}

// harvestCheck D-341 S1（自 engine_position.go 纯搬移）：XASSET 收敛止盈——spread_now ≤
// harvest_floor（monitor 滚动 q25 分位数收割线，MIN_CAPTURE 防贴线 churn 由 monitor 计算侧保证）
// → 带底收割。monitor 缺行/floor NULL → 不触发（诚实空态）。pairKey=D-335 同款（CanonicalSell-TrimSuffix）。
func (e *Engine) harvestCheck(ctx context.Context, p *pgstore.Position) (bool, string) {
	pairKey := canonicalFromOppID(p.OpportunityID)
	opp, err := e.store.GetOpportunity(ctx, p.OpportunityID)
	if err != nil || opp == nil {
		return false, ""
	}
	if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		pairKey = *opp.CanonicalSell + "-" + strings.TrimSuffix(opp.Canonical, "USD")
	}
	entryBps, _, floor, hasFloor, ok, err := e.store.LatestXAssetEntryGate(ctx, pairKey, xassetEntryGateMaxAge)
	if err != nil || !ok || !hasFloor {
		return false, ""
	}
	if entryBps <= floor {
		return true, fmt.Sprintf("harvest: spread %.2f ≤ floor %.2f (+%.2f bps captured)", entryBps, floor, floor-entryBps)
	}
	return false, ""
}
