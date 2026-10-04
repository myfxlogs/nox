// engine_xasset_eval.go —— D-377 S2：XASSET 退出评估观测轨。XASSET 分支自 engine_position.go
// 整组移入（纯搬移零行为变更）+ 评估行写入（评估之后、动作之前，hold/触发/fail_closed 全落行）。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"strings"
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
// D-053 R1②：财历禁区判定置顶本链（macroBlackoutSuspend 的 XASSET 豁免已拆）——
// 禁区内 carry_death 同 mtLegMarketClosed 径（ok=false、breach 计数原样保留）、
// harvest/execDrift/rotate 整点跳过，disaster 保命链仍跑；观测行 decision=macro_blackout。
func (e *Engine) xassetExitCheck(ctx context.Context, p *pgstore.Position) (bool, string) {
	blackoutHit, blackoutWhy := e.macroBlackoutActive(ctx, e.positionCanonical(ctx, p))
	// D-027：breach 量=滚动 3 日均值（瞬时费率周末零误杀实证）；MT 腿休市不累计 breach
	// （trade_mode fact 非 Full → 本轮按缺数据语义过判定器=计数原样保留）。
	netCarry, ok := e.recalcXAssetNetCarry(ctx, p, carryDeathMeanWindow)
	if ok && (e.mtLegMarketClosed(ctx, p) || blackoutHit) {
		ok = false // 闭市/禁区同径：本轮 carry_death 判据不可用=计数冻结（factOK=false）
	}
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
	if !shouldClose && !blackoutHit {
		shouldHarvest, harvestReason = e.harvestCheck(ctx, p)
	}
	// D-014 ②：feeAmortizeGate 已删——econ.NetIfClosedNow 直接含全账费用，
	// 净负永不收割由 harvestCheck 内部判定承担（eval 行不再出现 hold_fee_amortize）。
	shouldDrift, driftReason := false, ""
	if !shouldClose && !blackoutHit && !shouldHarvest {
		shouldDrift, driftReason = e.execDriftCheck(ctx, p)
	}
	shouldDisaster, disasterReason, stale, staleAge := false, "", false, time.Duration(0)
	if !shouldClose && !shouldHarvest && !shouldDrift {
		// D-378b S2：灾难带（替换 basisStopCheckXAsset 的位置与角色；窄阈撤除）。
		// D-053 R1②：禁区内仍跑——保命退出不被常规推迟闸延迟。
		shouldDisaster, disasterReason, stale, staleAge = e.disasterCheckXAsset(ctx, p)
	}
	// D-406 S1：轮转第五闸（链尾——四闸全过且非 stale 才评；先保命再优化）。
	var rot *rotateOutcome
	if !shouldClose && !blackoutHit && !shouldHarvest && !shouldDrift && !shouldDisaster && !stale {
		_, rot = e.rotateCheck(ctx, p) // triggered 在 rotateCheck 内置位
	}

	// D-377 S2：观测行——decision=本次评估结论；detail=阈值/reason 短串（store 侧 200 截断）。
	// fail_closed 也落行（quote 缺/锚缺/腿缺/stale=「评估跑了但无法判」正证据）。
	e.recordXAssetEval(ctx, p, xassetEvalOutcome{
		carryDeath: shouldClose, carryDeathReason: reason,
		harvest: shouldHarvest, harvestReason: harvestReason,
		drift: shouldDrift, driftReason: driftReason,
		disaster: shouldDisaster, disasterReason: disasterReason,
		stale: stale, staleAge: staleAge,
		macroBlackout: blackoutHit, macroBlackoutWhy: blackoutWhy,
		rotate: rot,
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
	if shouldDisaster {
		return true, disasterReason
	}
	if rot != nil && rot.triggered {
		return true, "rotate_to:" + rot.altOppID
	}
	return false, ""
}

// xassetEvalOutcome 评估链四判结果（观测行 decision 归并可测）。
// D-378b S2：stop 族改 disaster（灾难带替换窄阈）；stale=双腿报价超龄（不判灾难只留观测）。
type xassetEvalOutcome struct {
	carryDeath       bool
	carryDeathReason string
	harvest          bool
	harvestReason    string
	drift            bool
	driftReason      string
	disaster         bool
	disasterReason   string
	stale            bool
	staleAge         time.Duration
	// macroBlackout D-053 R1②：禁区内评估标记（why=事件名/BJ 时点/窗宽）。
	macroBlackout    bool
	macroBlackoutWhy string
	// rotate D-406：第五闸结果（nil=本轮未评——前四闸已触发/stale）。
	rotate *rotateOutcome
}

// recordXAssetEval D-377 S2：落观测行。basis=currentBasisBps 同函数复用（公式单点；
// in-memory quote 读代价可忽略）；fill=EntrySpreadBpsFromLegs 现成重算。
// decision 归并序：carry_death/harvest/exec_drift/disaster（评估已出结论，序=链序；
// D-378b：stop 族改 disaster）→ stale（fail_closed:stale，报价超龄不判灾难）→
// fail_closed 族（no_basis→no_anchor→no_legs）→ hold。
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
	case out.stale:
		decision, detail = "fail_closed:stale", fmt.Sprintf("quote age %s > max %s; band=%s",
			out.staleAge.Round(time.Second), e.cfg.XAssetQuoteMaxAge, fmtDisasterBand(e.cfg.XAssetDisasterBps, e.cfg.XAssetDisasterNoiseMult, p.EntryNoiseBps))
	case out.disaster:
		decision, detail = "disaster", out.disasterReason
	case out.macroBlackout:
		// D-053 R1②：禁区 hold 显影——detail=why（事件名/BJ 时点/窗宽）；stale/
		// disaster 更优已先行显影（保命/缺数据证据不掩）。
		decision, detail = "macro_blackout", out.macroBlackoutWhy
	case !basisOK:
		decision, detail = "fail_closed:no_basis", fmtDisasterBand(e.cfg.XAssetDisasterBps, e.cfg.XAssetDisasterNoiseMult, p.EntryNoiseBps)
	case p.ExpectedEntrySpreadBps == nil:
		decision, detail = "fail_closed:no_anchor", fmtDisasterBand(e.cfg.XAssetDisasterBps, e.cfg.XAssetDisasterNoiseMult, p.EntryNoiseBps)
	case !fillOK:
		decision, detail = "fail_closed:no_legs", fmtDisasterBand(e.cfg.XAssetDisasterBps, e.cfg.XAssetDisasterNoiseMult, p.EntryNoiseBps)
	case out.rotate != nil && out.rotate.triggered:
		decision, detail = "rotate", out.rotate.detail
	case out.rotate != nil:
		// D-406：轮转评了未触发 → hold_rotate_check（detail 含 alt/cur/cost 三值+拦阻因）。
		decision, detail = "hold_rotate_check", out.rotate.detail
	case strings.HasPrefix(out.harvestReason, "hold_"):
		// D-014：hold_econ 为收割 fail-closed 留痕——仅当其余判定均未出结论时显影
		// （econ 缺账=「不收割」，不阻断轮转/漂移/灾难等退出判定的观测优先级）。
		decision, detail = out.harvestReason, fmtDisasterBand(e.cfg.XAssetDisasterBps, e.cfg.XAssetDisasterNoiseMult, p.EntryNoiseBps)
	default:
		decision, detail = "hold", fmtDisasterBand(e.cfg.XAssetDisasterBps, e.cfg.XAssetDisasterNoiseMult, p.EntryNoiseBps)
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
