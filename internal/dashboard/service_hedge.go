// D-212 对冲工作台：GetHedgeStructure + ListHedgeFlow RPC handler（SSOT §1/§2）。
package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/collect/cmc"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetHedgeStructure 组装对冲结构卡（SSOT §1）：镜像双腿+净敞口真实计算+carry+双账户摘要。
// 数据源：fin_trx_account（质押腿 18070）+ fin_cmc_positions（空单腿 18100）+ fin_cmc_prices（mid）+
// fin_cmc_account_summary（强平价）+ position_daily_income（carry 全源聚合）+ fin_cmc_history（隔夜息）。
// 空表容忍：任一源空 → 对应字段零值/nil（前端显示「数据未就绪」）。
func (s *Service) GetHedgeStructure(ctx context.Context, req *connect.Request[v1.GetHedgeStructureRequest]) (*connect.Response[v1.GetHedgeStructureResponse], error) {
	resp := &v1.GetHedgeStructureResponse{}

	// TRX 账户快照（质押腿数量源=votes_total=18070，v1.2 裁定）
	trxAcc, trxErr := s.store.LastTRXAccount(ctx)
	hasTRX := trxErr == nil

	// CMC 空单持仓（entry_price=baseline，quantity=空单腿 qty）
	cmcPos, cmcPosErr := s.store.LastCMCPositionByInstrument(ctx, "X-AFQUL")
	hasCMCPos := cmcPosErr == nil

	// CMC 账户摘要（closeout_price=强平价，distance_to_closeout_pct=距强平%）
	cmcSum, cmcSumErr := s.store.LastCMCAccountSummary(ctx)
	hasCMCSum := cmcSumErr == nil

	// D-225 v2.1：mid 显示用 REST mid（LastCMCPriceMidWithTime 加 NULL mid 过滤）；
	// 镜像 PnL 用 ForSide(short)=ask 评估价口径（卡片标注）。
	var mid float64
	var evalAsk float64
	if hasCMCPos {
		if v, _, err := s.store.LastCMCPriceMidWithTime(ctx, "X-AFQUL"); err == nil {
			mid = v
		}
		if px, _, _, err := s.store.LastCMCPriceForSide(ctx, "X-AFQUL", "Sell"); err == nil {
			evalAsk = px
		}
	}

	// baseline=空单开仓价（fin_cmc_positions.entry_price；现货购价未知，注释标注）
	var baseline float64
	if hasCMCPos {
		baseline = cmcPos.EntryPrice
	}

	// 质押腿（leg_spot）：qty_staked=votes_total（展示），qty_liquid=balance，
	// pnl=(mid−baseline)×基准qty（镜像腿——基准qty=空单qty，两腿同 qty 确保恒和=0）
	legSpot := &v1.HedgeLegSpot{Label: "质押现货"}
	if hasTRX {
		qtyStaked := float64(trxAcc.VotesTotal)
		legSpot.QtyStaked = qtyStaked
		legSpot.QtyLiquid = float64(trxAcc.BalanceSun) / 1e6
		legSpot.Stake = &v1.HedgeStake{
			Energy:    float64(trxAcc.EnergyDelegated) / 1e6,
			Bandwidth: float64(trxAcc.BandwidthStaked) / 1e6,
		}
	}

	// 空单腿（leg_short）：qty=CMC quantity（基准 qty），pnl=(baseline−evalAsk)×qty（镜像腿，恒和=0）
	// D-225 v2.1：PnL 用 evalAsk（ForSide short=ask 评估价口径）；evalAsk=0 时回退 mid。
	pnlPx := evalAsk
	if pnlPx == 0 {
		pnlPx = mid
	}
	legShort := &v1.HedgeLegShort{Label: "CMC 空单"}
	var mirrorQty float64 // 基准 qty（空单 qty），两腿镜像 pnl 共用
	if hasCMCPos {
		mirrorQty = cmcPos.Quantity
		legShort.Qty = mirrorQty
		if pnlPx > 0 && baseline > 0 {
			legShort.PnlVsBase = (baseline - pnlPx) * mirrorQty
		}
		if cmcPos.MarginPercentage > 0 {
			legShort.Lev = int32(1.0 / cmcPos.MarginPercentage)
		}
		legShort.BrokerUnrealised = cmcPos.UnrealisedPnL
	}
	// 质押腿镜像 pnl（用空单 qty 确保恒和=0；qty_staked 仅展示质押数量）
	if hasTRX && pnlPx > 0 && baseline > 0 && mirrorQty > 0 {
		legSpot.PnlVsBase = (pnlPx - baseline) * mirrorQty
	}
	if hasCMCSum {
		legShort.Liq = cmcSum.CloseoutPrice
		legShort.DistPct = cmcSum.DistanceToCloseoutPct
	}

	// 净敞口（v1.2 真实计算：(balance+staked−short_qty)×mid）
	var netExposureTrx, netExposureUsd float64
	if hasTRX && hasCMCPos {
		balance := float64(trxAcc.BalanceSun) / 1e6
		staked := float64(trxAcc.VotesTotal)
		netExposureTrx = balance + staked - cmcPos.Quantity
		netExposureUsd = netExposureTrx * mid
	}

	// carry（近 7 日 + 累计 + 年化）
	carry := s.computeHedgeCarry(ctx, mid, legSpot.QtyStaked)

	// 双账户摘要
	accts := &v1.HedgeAccounts{}
	var updatedAt time.Time
	if hasTRX {
		accts.TrxBalance = float64(trxAcc.BalanceSun) / 1e6
		accts.TrxAllowance = float64(trxAcc.AllowanceSun) / 1e6
		accts.EnergyDelegated = float64(trxAcc.EnergyDelegated) / 1e6
		accts.Votes = float64(trxAcc.VotesTotal)
		updatedAt = trxAcc.FetchedAt
	}
	if hasCMCSum {
		accts.CmcCash = cmcSum.TotalCashBalance
		accts.CmcEquity = cmcSum.TotalAccountValue
		if cmcSum.FetchedAt.After(updatedAt) {
			updatedAt = cmcSum.FetchedAt
		}
	}
	if !updatedAt.IsZero() {
		accts.UpdatedAt = timestamppb.New(updatedAt)
	}

	resp.Structure = &v1.HedgeStructure{
		BaselinePrice:      baseline,
		Mid:                mid,
		LegSpot:            legSpot,
		LegShort:           legShort,
		NetExposureTrx:     netExposureTrx,
		NetExposureUsd:     netExposureUsd,
		Carry:              carry,
		Accts:              accts,
		CloseoutSource:     "estimate_self", // D-225 v2.1：过渡期自算估值标注（Phase B 后切平台值）
		PlatformAlertState: cmc.AlertsToState(cmcSum.Alerts),
		PlatformAlerts:     string(cmcSum.Alerts),
	}
	// D-193-A/R9：CMC ws 事件驱动健康（nil=未接线→false=黄点 REST 兜底）
	if s.cmcWsHealth != nil {
		resp.Structure.WsConnected = s.cmcWsHealth()
	}
	return connect.NewResponse(resp), nil
}

// computeHedgeCarry 计算 carry 统计（近 7 日 + 累计 + 年化）。
// cum_usd = 全量 (energy+voting)×price + interest（price 用当期 mid 近似——历史日 price 快照在 daily_income.price）；
// annualized_pct = cum_usd / (qty×mid) × 365（年化分母=现货市值，A1）。
func (s *Service) computeHedgeCarry(ctx context.Context, mid, qtyStaked float64) *v1.HedgeCarry {
	now := time.Now().UTC()
	from7d := now.Add(-7 * 24 * time.Hour)

	// 近 7 日 energy/voting（全源聚合）
	energy7d, voting7d, _, err := s.store.SumDailyIncomeByRange(ctx, &from7d, &now)
	if err != nil {
		energy7d, voting7d = 0, 0
	}
	// 近 7 日隔夜息
	overnight7d, err := s.store.SumCMCOvernightByRange(ctx, &from7d, &now)
	if err != nil {
		overnight7d = 0
	}
	// 累计全量
	energyAll, votingAll, interestAll, err := s.store.SumDailyIncomeByRange(ctx, nil, nil)
	if err != nil {
		energyAll, votingAll, interestAll = 0, 0, 0
	}
	cumUsd := (energyAll+votingAll)*mid + interestAll

	// 年化（分母=现货市值 qty×mid）
	var annualized float64
	if mid > 0 && qtyStaked > 0 {
		annualized = cumUsd / (qtyStaked * mid) * 365 * 100 // 百分数
	}

	return &v1.HedgeCarry{
		TrxRental_7DTrx:    energy7d,
		TrxVote_7DTrx:      voting7d,
		CmcOvernight_7DUsd: overnight7d,
		CumUsd:             cumUsd,
		AnnualizedPct:      annualized,
	}
}

// ListHedgeFlow 统一流水卡（SSOT §2）：UNION ALL 跨源分页（fin_trx_txs + fin_cmc_history）。
// sourceFilter=” = 全部；'trx'/'cmc' = 单源。kind 归一 8 类（A5：SELL/BUY_TRADE→other）。
func (s *Service) ListHedgeFlow(ctx context.Context, req *connect.Request[v1.ListHedgeFlowRequest]) (*connect.Response[v1.ListHedgeFlowResponse], error) {
	limit := int(req.Msg.Limit)
	if limit <= 0 {
		limit = 50
	}
	offset := int(req.Msg.Offset)
	items, err := s.store.ListHedgeFlowPage(ctx, req.Msg.SourceFilter, limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list hedge flow: %w", err))
	}
	resp := &v1.ListHedgeFlowResponse{}
	for _, it := range items {
		resp.Txs = append(resp.Txs, &v1.HedgeFlowItem{
			Source:       it.Source,
			Ts:           timestamppb.New(it.Ts),
			Kind:         it.Kind,
			AmountValue:  it.AmountValue,
			AmountUnit:   it.AmountUnit,
			Counterparty: it.Counterparty,
			TxId:         it.TxID,
		})
	}
	return connect.NewResponse(resp), nil
}
