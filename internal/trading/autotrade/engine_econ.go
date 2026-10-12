// engine_econ.go —— D-014：econ.Snapshot 装配面（持仓=实账 + 入场=估算），
// 收编自 engine_fee_amortize.go 的共用 helper（accrued/venue 对分类）。
// 费率源：venue 事实/配置（cfg.EconFeeBps，装配在 cmd/core）；缺=fail-closed 拒，禁默认值。
// pair-key/venue 分类 helpers 见 engine_econ_pair.go（D-024 拆分 ≤300 契约）。
package autotrade

import (
	"context"
	"fmt"
	"time"

	"arb/internal/data/keydomain"
	"arb/internal/research/econ"
	"arb/internal/risk/gate"
	"arb/internal/store/pgstore"
)

// econFeeBps 腿费率解析（venue→bps；resolver 未接线/无源 → ok=false）。
func (e *Engine) econFeeBps(ctx context.Context, venueKey string) (float64, bool) {
	if e.cfg.EconFeeBps == nil {
		return 0, false
	}
	return e.cfg.EconFeeBps(ctx, venueKey)
}

// econExitFeeBps D-018 后段①：腿退出费率按 fill_kind 选——maker 腿取 maker 费率
// （exchange_configs.maker_fee_bps），缺源回落 taker 保守；taker/NULL/未知 → taker。
// taker 本身缺 → ok=false fail-closed（与 D-014 同规）。
func (e *Engine) econExitFeeBps(ctx context.Context, l pgstore.PositionLeg) (float64, bool) {
	bps, ok := e.econFeeBps(ctx, l.Venue)
	if !ok {
		return 0, false
	}
	if l.FillKind != nil && *l.FillKind == "maker" && e.cfg.EconMakerFeeBps != nil {
		if mbps, mok := e.cfg.EconMakerFeeBps(ctx, l.Venue); mok {
			bps = mbps
		}
	}
	return bps, true
}

// legNotionalUSD 腿名义 USD = EffectiveQty × USDPerLotForLeg（报价/规格缺→false fail-closed）。
func (e *Engine) legNotionalUSD(canonical string, l pgstore.PositionLeg) (float64, bool) {
	if e.service == nil {
		return 0, false
	}
	side := "ask"
	if l.Side == "SELL" {
		side = "bid"
	}
	perLot, ok := e.service.USDPerLotForLeg(canonical, l.Venue, side)
	if !ok || perLot <= 0 {
		return 0, false
	}
	return l.EffectiveQty() * perLot, true
}

// mtHalfSpreadCostUSD MT 腿半点差成本 = legUSD × (ask−bid)/(ask+bid)。
// 报价缺/超龄（XAssetQuoteMaxAge）→ error fail-closed。
func (e *Engine) mtHalfSpreadCostUSD(legUSD float64, canonical, venueKey string) (float64, error) {
	if e.quotes == nil {
		return 0, fmt.Errorf("quote bus 未接线")
	}
	q, ok := e.quotes.Latest(canonical, venueKey)
	if !ok || q.Bid <= 0 || q.Ask < q.Bid {
		return 0, fmt.Errorf("%s %s 报价缺/非法", canonical, venueKey)
	}
	if e.cfg.XAssetQuoteMaxAge > 0 && time.Since(q.Time) > e.cfg.XAssetQuoteMaxAge {
		return 0, fmt.Errorf("%s %s 报价超龄 %v", canonical, venueKey, time.Since(q.Time))
	}
	return legUSD * (q.Ask - q.Bid) / (q.Ask + q.Bid), nil
}

// econSnapshot 持仓经济快照（harvestCheck 决策源）。缺腿/缺费率/缺报价 → error fail-closed。
func (e *Engine) econSnapshot(ctx context.Context, p *pgstore.Position) (econ.Snapshot, error) {
	var snap econ.Snapshot
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return snap, fmt.Errorf("legs: %w", err)
	}
	buyCanonical := canonicalFromOppID(p.OpportunityID)
	if buyCanonical == "" {
		return snap, fmt.Errorf("canonical 解析失败 opp=%s", p.OpportunityID)
	}
	sellCanonical := e.xassetSellCanonical(ctx, p, legs)
	if sellCanonical == "" {
		sellCanonical = buyCanonical // 同 canonical 构型（SWAPARB/FUNDING_HEDGE）回退
	}
	paid, hasRows, err := e.store.CommissionPaidUSD(ctx, p.ID)
	if err != nil {
		return snap, fmt.Errorf("commission cashflows: %w", err)
	}
	accrued, err := xassetAccruedCarryUSD(ctx, e.store, p.ID)
	if err != nil {
		return snap, fmt.Errorf("accrued cashflows: %w", err)
	}
	snap.AccruedUSD = accrued
	netCarryPct, ok := e.recalcXAssetNetCarry(ctx, p, carryEconWindow, "exit") // D-027：7d 滚动均值
	if !ok {
		return snap, fmt.Errorf("carry facts 缺（fail-closed）")
	}
	buyN, exitEst, legFeeEst := 0.0, 0.0, 0.0
	for i := range legs {
		l := legs[i]
		if l.Phase != "OPEN" || l.Status != "FILLED" {
			continue
		}
		canonical := buyCanonical
		if l.Side != "BUY" {
			canonical = sellCanonical
		}
		n, ok := e.legNotionalUSD(canonical, l)
		if !ok {
			return snap, fmt.Errorf("leg %d %s %s 名义不可得", l.ID, l.Venue, l.Symbol)
		}
		if l.Side == "BUY" {
			buyN += n
		}
		if keydomain.IsMTKey(l.Venue) {
			hs, err := e.mtHalfSpreadCostUSD(n, canonical, l.Venue)
			if err != nil {
				return snap, fmt.Errorf("MT 半点差: %w", err)
			}
			exitEst += hs
		} else {
			bps, ok := e.econExitFeeBps(ctx, l) // D-018 后段①：fill_kind=maker 腿取 maker 费率
			if !ok {
				return snap, fmt.Errorf("taker 费率缺 %s（venue 事实/配置均无）", l.Venue)
			}
			exitEst += n * bps / 1e4
		}
		if !hasRows { // legacy 零 commission 行仓（D-013 前）：腿级估算回退
			if l.FeeUSD != nil {
				legFeeEst += *l.FeeUSD
			} else if bps, ok := e.econFeeBps(ctx, l.Venue); ok {
				legFeeEst += n * bps / 1e4
			} else {
				return snap, fmt.Errorf("费率缺 %s", l.Venue)
			}
		}
	}
	if buyN <= 0 {
		return snap, fmt.Errorf("无 BUY 腿/名义为 0")
	}
	snap.EntryCostUSD = paid
	if !hasRows {
		snap.EntryCostUSD = legFeeEst
	}
	snap.ExitCostEstUSD = exitEst
	snap.CarryPerDayUSD = netCarryPct / 100 / 365 * buyN
	// Capture：锚缺或报价不可用 → 0（保守：不计收敛利润，宁少收不误收）。
	if p.ExpectedEntrySpreadBps != nil && *p.ExpectedEntrySpreadBps != 0 {
		if basis, age, okB := e.currentBasisBpsAged(p); okB &&
			(e.cfg.XAssetQuoteMaxAge <= 0 || age <= e.cfg.XAssetQuoteMaxAge) {
			snap.CaptureUSD = (basis - *p.ExpectedEntrySpreadBps) * buyN / 10000
		}
	}
	return snap, nil
}

// econEntrySnapshot 候选入场快照（engine_opp 入场闸）：两腿成本全估算，Capture/Accrued=0。
// legNotionalUSD=单腿名义（名义相等对冲）。carry/费率/MT 报价缺 → error fail-closed 拒开。
func (e *Engine) econEntrySnapshot(ctx context.Context, opp pgstore.Opportunity, sellCanonical string, legNotionalUSD float64) (econ.Snapshot, error) {
	var snap econ.Snapshot
	if legNotionalUSD <= 0 {
		return snap, fmt.Errorf("legNotional %.2f ≤0", legNotionalUSD)
	}
	if sellCanonical == "" {
		sellCanonical = opp.Canonical
	}
	maxAge := e.cfg.SwapFactMaxAge
	buyCarry, ok := e.xassetLegCarryAnnual(ctx, opp.VenueBuy, opp.Canonical, "BUY", maxAge, carryEconWindow)
	if !ok {
		return snap, fmt.Errorf("买腿 carry 缺 %s %s", opp.VenueBuy, opp.Canonical)
	}
	sellCarry, ok := e.xassetLegCarryAnnual(ctx, opp.VenueSell, sellCanonical, "SELL", maxAge, carryEconWindow)
	if !ok {
		return snap, fmt.Errorf("卖腿 carry 缺 %s %s", opp.VenueSell, sellCanonical)
	}
	snap.CarryPerDayUSD = (buyCarry + sellCarry) / 100 / 365 * legNotionalUSD
	for _, leg := range [][2]string{{opp.VenueBuy, opp.Canonical}, {opp.VenueSell, sellCanonical}} {
		vk, canon := leg[0], leg[1]
		bps, ok := e.econFeeBps(ctx, vk)
		if !ok {
			return snap, fmt.Errorf("费率缺 %s（venue 事实/配置均无，fail-closed 拒开）", vk)
		}
		snap.EntryCostUSD += legNotionalUSD * bps / 1e4 // 入场费：CEX taker / MT 佣金
		if keydomain.IsMTKey(vk) {
			hs, err := e.mtHalfSpreadCostUSD(legNotionalUSD, canon, vk)
			if err != nil {
				return snap, fmt.Errorf("MT 半点差: %w", err)
			}
			snap.ExitCostEstUSD += hs // MT 腿退出=实时半点差
		} else {
			snap.ExitCostEstUSD += legNotionalUSD * bps / 1e4 // CEX 腿退出=taker
		}
	}
	return snap, nil
}

// EntryBreakevenDaysPublic D-014 ④：pending_entries 回本估（econ 同源装配，供 dashboard setter 注入）。
func (e *Engine) EntryBreakevenDaysPublic(ctx context.Context, venueBuy, venueSell, canonBuy, canonSell string, legNotionalUSD float64) (float64, bool) {
	opp := pgstore.Opportunity{Canonical: canonBuy, VenueBuy: venueBuy, VenueSell: venueSell}
	snap, err := e.econEntrySnapshot(ctx, opp, canonSell, legNotionalUSD)
	if err != nil || snap.CarryPerDayUSD <= 0 {
		return 0, false
	}
	return snap.BreakevenDays(), true
}

// PositionEconPublic D-014 ④：持仓回本列同源——装 posID → econSnapshot（dashboard 注入，
// 回本列与收割闸共一结构；err/缺账 → 调用方缺省显示 fail-closed 不猜）。
func (e *Engine) PositionEconPublic(ctx context.Context, posID int64) (econ.Snapshot, error) {
	p, err := e.store.GetPosition(ctx, posID)
	if err != nil || p == nil {
		return econ.Snapshot{}, fmt.Errorf("position %d: %v", posID, err)
	}
	return e.econSnapshot(ctx, p)
}

// xassetAccruedCarryUSD 本仓已落 cashflows 的 funding/swap 净额（actual 口径；
// commission 不算 carry——费用由 EntryCostUSD 侧计防双计）。自 engine_fee_amortize.go 收编。
func xassetAccruedCarryUSD(ctx context.Context, store *pgstore.Store, posID int64) (float64, error) {
	cfs, err := store.ListCashflows(ctx, posID)
	if err != nil {
		return 0, err
	}
	var sum float64
	for _, c := range cfs {
		if c.Kind != "funding" && c.Kind != "swap" {
			continue
		}
		if c.Quality != nil && *c.Quality != "actual" {
			continue // estimated/unknown 不入实收
		}
		sum += c.AmountUSD
	}
	return sum, nil
}

// xassetLegVenues D-423：机会 venue 两字段→(cexVenue,mtVenue)。:mt4/:mt5 后缀判 MT；
// 无 MT 腿→mtVenue=""（CEX×CEX/反向形态 monitor 方向未定义，查询自然 miss=fail-closed）。
func xassetLegVenues(buy, sell string) (cexVenue, mtVenue string) {
	switch {
	case gate.IsMTVenue(buy):
		return sell, buy
	case gate.IsMTVenue(sell):
		return buy, sell
	default:
		return sell, ""
	}
}
