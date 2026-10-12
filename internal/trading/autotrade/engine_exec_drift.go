// engine_exec_drift.go —— D-378a S2：XASSET 执行质量闸（exec_drift）+ harvestCheck 整组移入
// （engine_position.go 300 行门禁；harvest 为纯搬移零行为变更，XASSET 退出链出口同文件族）。
package autotrade

import (
	"context"
	"fmt"
	"math"

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

// harvestCheck D-014 ②（替换 D-341 收割线/q25 族）：收敛止盈走 econ.Snapshot——
// NetIfClosedNow ≥0 ∧ ≥ CarryPerDay×HarvestHorizonDays（锁定现在的收敛利润须优于
// 再持一周 carry）；NetIfClosedNow<0 永不收割（亏损出场只走 disaster/carry_death）。
// 装配缺账（费率/报价/carry facts/现金流行任一缺）→ 不收割 fail-closed，reason
// hold_econ:<因> 进 eval 观测（沿用 hold_ 前缀族）。
func (e *Engine) harvestCheck(ctx context.Context, p *pgstore.Position) (bool, string) {
	snap, err := e.econSnapshot(ctx, p)
	if err != nil {
		return false, "hold_econ:" + err.Error()
	}
	net := snap.NetIfClosedNow()
	horizon := e.cfg.HarvestHorizonDays
	if horizon <= 0 {
		horizon = 7.0
	}
	if snap.HarvestOK(horizon) {
		return true, fmt.Sprintf("harvest: net_if_close %.2f ≥ carry %.2f×%.0fd (entry %.2f exit %.2f accrued %.2f capture %.2f)",
			net, snap.CarryPerDayUSD, horizon, snap.EntryCostUSD, snap.ExitCostEstUSD, snap.AccruedUSD, snap.CaptureUSD)
	}
	return false, ""
}
