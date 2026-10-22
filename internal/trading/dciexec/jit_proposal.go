// jit_proposal.go —— 提案轨 jit 归集前置（D-658 业主诊「jit 没接通」执行面）：
// GoProposal 腿执行前，投资币=USDT 提案且 venue∈jit 白名单时——扣款户可动
// <票面需求→JitSweep 复用件归集（armed 径同件零分叉，禁第三实现）；
// 败按 JitErrKind 落请求箱（携目标桶+额锚=armed 径落箱三件套同形）。
package dciexec

import (
	"context"
	"fmt"
	"log"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// jitSweepDo jit 归集调用缝——测试注 JitSweepFn，nil=JitSweep 实件（armed
// 径与本径同一 Executor 方法零分叉）。
func (e *Executor) jitSweepDo(ctx context.Context, venueKey string, shortfall, oppAPR float64) error {
	if e.JitSweepFn != nil {
		return e.JitSweepFn(ctx, venueKey, shortfall, oppAPR)
	}
	return e.JitSweep(ctx, venueKey, shortfall, oppAPR)
}

// jitProposalSweep GoProposal 归集前置段（票面①）：USDT 投资币腿提案→
// 可动<票面需求时发 JitSweep（shortfall=需求−可动，oppAPR=评估卡 net_apr）。
// 收益比对闸不绕（JitSweep 内 D-647 fail-closed 语义）；earn 申购族反向提案
// （spot→earn）与 CALL 类 BTC 担保品腿不接本段（票面③④具名另案/语义正确）。
func (e *Executor) jitProposalSweep(ctx context.Context, p *pgstore.Proposal, card *pgstore.OppEval, share float64) error {
	if !jitVenues[p.Venue] {
		return nil
	}
	hasUSDT := false
	for _, l := range gate.ExecTemplates[p.TemplateID] {
		if l.QtyCcy == jitCcy {
			hasUSDT = true
		}
	}
	if !hasUSDT {
		return nil
	}
	demand, avail, err := e.proposalUSDTDemand(ctx, p.Venue, share)
	if err != nil {
		return fmt.Errorf("提案#%d jit 前置余额读败（fail-closed）: %w", p.ID, err)
	}
	if avail >= demand-1e-6 {
		return nil // 可动足=照走（腿径自带重读续执）
	}
	oppAPR := 0.0
	if card != nil && card.NetAPR != nil {
		oppAPR = *card.NetAPR
	}
	// DCI 扣款户域：binance=spot、okx=funding——spot vt 免二跳恰落扣款户。
	venueKey := p.Venue + ":spot"
	shortfall := demand - avail
	if err := e.jitSweepDo(ctx, venueKey, shortfall, oppAPR); err != nil {
		kind := JitErrKind(err)
		gap := &pgstore.ConditionGap{
			TemplateID: p.TemplateID, Kind: kind, Subject: venueKey,
			RequestText: fmt.Sprintf("提案#%d USDT 缺资 jit 归集败（%s 需求 %.4f 可动 %.4f）: %v",
				p.ID, venueKey, demand, avail, err)}
		if kind == "capital" { // D-652-C：核验锚=目标桶+额（armed 径同形三件套）
			gap.TargetBucket = venueKey
			gap.TargetUSD = &shortfall
		}
		if id, gerr := e.Store.OpenConditionGap(ctx, gap); gerr == nil {
			_ = e.Store.RequestConditionGap(ctx, id)
		}
		return fmt.Errorf("提案#%d jit 归集败（%s 落箱）: %w", p.ID, kind, err)
	}
	log.Printf("[dciexec] jit 提案轨归集 提案#%d %s %.4f %s 成——腿径重读续执",
		p.ID, venueKey, shortfall, jitCcy)
	return nil
}

// proposalUSDTDemand 票面需求/扣款户可动双读（venue 分派——口径与腿径池公式
// 同源：binance=poolBalance×share vs spotFree；okx=(unified+funding+savings)
// ×share vs unified+funding〔savings 属 earn 在册，归集消费面不双计〕）。
func (e *Executor) proposalUSDTDemand(ctx context.Context, venue string, share float64) (demand, avail float64, err error) {
	switch venue {
	case "binance":
		pool, err := e.poolBalance(ctx, jitCcy)
		if err != nil {
			return 0, 0, err
		}
		free, err := e.freeBalance(ctx, jitCcy)
		if err != nil {
			return 0, 0, err
		}
		return pool * share, free, nil
	case "okx":
		cred, ok := e.EarnCreds["okx"]
		if !ok || cred.Key == "" {
			return 0, 0, fmt.Errorf("okx 凭据行缺")
		}
		rail := &okxEarnRail{e: e, c: cred}
		u, err := rail.unifiedAvail(ctx, jitCcy)
		if err != nil {
			return 0, 0, err
		}
		f, err := rail.fundingAvail(ctx, jitCcy)
		if err != nil {
			return 0, 0, err
		}
		sav, err := rail.savingsAvail(ctx, jitCcy)
		if err != nil {
			return 0, 0, err
		}
		return (u + f + sav) * share, u + f, nil
	}
	return 0, 0, fmt.Errorf("jit 前置 venue %s 未支", venue)
}
