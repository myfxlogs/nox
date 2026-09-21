// engine_xasset_eval.go —— D-377 S2：XASSET 退出评估观测轨。XASSET 分支自 engine_position.go
// 整组移入（纯搬移零行为变更）+ 评估行写入（评估之后、动作之前，hold/触发/fail_closed 全落行）。
package autotrade

import (
	"context"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

// xassetExitCheck XASSET_CARRY 退出判定（原 engine_position.go 分支逐字搬移）：
// carry_death（funding 判，minHold+去抖）→ harvest（收敛止盈）→ execDrift（执行质量闸）
// → basisStop（基差止损）。
// D-377 S2：判定完成后落一行观测行（无论 hold/触发/fail_closed）——观测在动作前、
// 写库失败 fail-open 不阻塞判定。
// D-378a 收编整合（Devin 复审）：本分支基线在 D-378a merge 前，收编时补 execDrift 步
// 入链（序位 harvest 后 stop 前，与 D-378a SSOT 钉序一致）+ outcome/decision 归并序对齐。
func (e *Engine) xassetExitCheck(ctx context.Context, p *pgstore.Position) (bool, string) {
	netCarry, ok := e.recalcXAssetNetCarry(ctx, p)
	var held time.Duration
	if p.OpenedAt != nil {
		held = time.Since(*p.OpenedAt)
	}
	e.fundingMu.Lock()
	curCount := e.fundingBreach[p.ID]
	shouldClose, newCount, reason := perpFundingExitDecision(ok, held, netCarry, e.cfg.FundingExitPct, e.cfg.PerpFundMinHold, e.cfg.PerpFundExitDebounce, curCount)
	if newCount == 0 {
		delete(e.fundingBreach, p.ID) // 计数清零/平仓 → 删除条目（防泄漏）
	} else {
		e.fundingBreach[p.ID] = newCount
	}
	e.fundingMu.Unlock()
	shouldHarvest, harvestReason := false, ""
	if !shouldClose {
		shouldHarvest, harvestReason = e.harvestCheck(ctx, p)
	}
	shouldDrift, driftReason := false, ""
	if !shouldClose && !shouldHarvest {
		shouldDrift, driftReason = e.execDriftCheck(ctx, p)
	}
	shouldStop, stopReason := false, ""
	if !shouldClose && !shouldHarvest && !shouldDrift {
		shouldStop, stopReason = e.basisStopCheckXAsset(p)
	}

	// D-377 S2：观测行——decision=本次评估结论；detail=阈值/reason 短串（store 侧 200 截断）。
	// fail_closed 也落行（quote 缺/锚缺/腿缺=「评估跑了但无法判」正证据）。
	e.recordXAssetEval(ctx, p, xassetEvalOutcome{
		carryDeath: shouldClose, carryDeathReason: reason,
		harvest: shouldHarvest, harvestReason: harvestReason,
		drift: shouldDrift, driftReason: driftReason,
		stop: shouldStop, stopReason: stopReason,
	})

	if shouldClose {
		return true, reason
	}
	if shouldHarvest {
		return true, harvestReason
	}
	if shouldDrift {
		return true, driftReason
	}
	return shouldStop, stopReason
}

// xassetEvalOutcome 评估链四判结果（观测行 decision 归并可测）。
type xassetEvalOutcome struct {
	carryDeath       bool
	carryDeathReason string
	harvest          bool
	harvestReason    string
	drift            bool
	driftReason      string
	stop             bool
	stopReason       string
}

// recordXAssetEval D-377 S2：落观测行。basis=currentBasisBps 同函数复用（公式单点；
// in-memory quote 读代价可忽略）；fill=EntrySpreadBpsFromLegs 现成重算。
// decision 归并序：carry_death/harvest/exec_drift/stop（评估已出结论，序=链序）→
// fail_closed 族（no_basis→no_anchor→no_legs，评估跑了但无法判）→ hold。
// basis/fill 两列无论结论都记录（D1 漂移序列源）。
// fail-open 钉注：写库失败 → warn 日志（观测丢失显式留痕）继续评估流程。
// 非 XASSET_CARRY 不写行——scope 闸在调用点（engine_position.go 一行 if）。
func (e *Engine) recordXAssetEval(ctx context.Context, p *pgstore.Position, out xassetEvalOutcome) {
	basisBps, basisOK := e.currentBasisBps(p)
	fillBps, fillOK, _ := e.store.EntrySpreadBpsFromLegs(ctx, p.ID)
	var decision, detail string
	switch {
	case out.carryDeath:
		decision, detail = "carry_death", out.carryDeathReason
	case out.harvest:
		decision, detail = "harvest", out.harvestReason
	case out.drift:
		decision, detail = "exec_drift", out.driftReason
	case out.stop:
		decision, detail = "stop", out.stopReason
	case !basisOK:
		decision, detail = "fail_closed:no_basis", fmtStopBps(e.cfg.BasisStopBps, p.EntryNoiseBps)
	case p.ExpectedEntrySpreadBps == nil:
		decision, detail = "fail_closed:no_anchor", fmtStopBps(e.cfg.BasisStopBps, p.EntryNoiseBps)
	case !fillOK:
		decision, detail = "fail_closed:no_legs", fmtStopBps(e.cfg.BasisStopBps, p.EntryNoiseBps)
	default:
		decision, detail = "hold", fmtStopBps(e.cfg.BasisStopBps, p.EntryNoiseBps)
	}
	var basis, fill *float64
	if basisOK {
		basis = &basisBps
	}
	if fillOK {
		fill = &fillBps
	}
	if err := e.store.InsertPositionEval(ctx, pgstore.PositionEval{
		PositionID: p.ID, BasisBps: basis, AnchorBps: p.ExpectedEntrySpreadBps, FillBps: fill,
		Decision: decision, Detail: detail,
	}); err != nil {
		log.Printf("[autotrade] WARN position eval record failed (fail-open, 观测丢失留痕) pos=%d: %v", p.ID, err)
	}
}
