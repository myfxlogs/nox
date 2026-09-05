// D-212 对冲工作台：GetHedgeStructure + ListHedgeFlow RPC handler（SSOT §1/§2）。
package dashboard

import (
	"context"
	"fmt"
	"math/big"
	"time"

	"arb/internal/collect/cmc"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"

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

	// D-231 S3：TRX 投票收益率（fin_trx_vote_apr 最新行透传，只渲染不运算）
	trxApr, trxAprErr := s.store.LastTRXVoteApr(ctx)
	hasTrxApr := trxAprErr == nil

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
	// D-231 S3：投票收益率透传（fin_trx_vote_apr 最新行，只渲染不运算）
	if hasTrxApr {
		legSpot.VoteAprCurrent = trxApr.CurrentRate
		legSpot.VoteAprMax = trxApr.MaxRate
		legSpot.VoteAprSpread = trxApr.Spread
		if trxApr.MaxSr != nil {
			legSpot.VoteAprMaxSr = *trxApr.MaxSr
		}
		legSpot.VoteAprTs = timestamppb.New(trxApr.FetchedAt)
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
		// D-233 S2：TrxAllowance 承载待领取语义（=allowance + 未结算应计 Vi×userVotes/10^18）。
		// 按 SR 年化积分估算，对齐 TronLink 口径；结算后与链上 allowance 收敛。
		// 精确复现不可行：reward(cycle,sr) 为节点内部量，公开 API 不暴露（R-D2 诚实化留档）。
		// 无 Vi 数据时回退纯 allowance（诚实降级，不阻断卡片展示）。
		accts.TrxAllowance = computePendingReward(ctx, s.store, trxAcc.AllowanceSun)
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

	var closeoutLevelUSD, warningLevelUSD float64
	if hasCMCSum {
		closeoutLevelUSD = cmcSum.TotalEquityUsedInMargin * 0.5
		warningLevelUSD = cmcSum.TotalEquityUsedInMargin * 0.8
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
		CloseoutSource:     "platform_rule_50", // D-225 v2.2：CMC 面板标定为保证金 50%
		PlatformAlertState: cmc.AlertsToState(cmcSum.Alerts),
		PlatformAlerts:     string(cmcSum.Alerts),
		CloseoutLevelUsd:   closeoutLevelUSD,
		WarningLevelUsd:    warningLevelUSD,
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

// computePendingReward D-233 S2：待领取 = allowance + Σ_SRs Vi × userVotes ÷ 10^18。
// Vi 从 fin_trx_cycle_vi 最新 cycle 读取（VotesCollector 按 SR 年化积分估算累积）。
// 按 SR 年化积分估算，对齐 TronLink 口径；精确复现不可行（reward(cycle,sr) 为节点内部量，R-D2 留档）。
// 无 Vi 数据（空表/错误）→ 回退纯 allowance（诚实降级，不阻断卡片展示）。
// 返回值单位=TRX（sun/1e6）。
func computePendingReward(ctx context.Context, store interface {
	LatestTRXCycleVi(context.Context) ([]pgstore.TRXCycleVi, error)
}, allowanceSun int64) float64 {
	allowanceTRX := float64(allowanceSun) / 1e6
	viRows, err := store.LatestTRXCycleVi(ctx)
	if err != nil || len(viRows) == 0 {
		return allowanceTRX // 无 Vi 数据→回退纯 allowance
	}
	// 待领取 = allowance + Σ_SRs Vi × userVotes / 10^18（sun）
	// = allowanceSun + Σ (vi × userVotes / 10^18)  → sun → /1e6 → TRX
	decimalOfViReward := new(big.Int).Exp(big.NewInt(10), big.NewInt(18), nil)
	totalUnsettledSun := new(big.Int)
	for _, r := range viRows {
		if r.Vi == nil || r.UserVotes <= 0 {
			continue
		}
		// unsettled = vi × userVotes / 10^18（big.Int 整除，与 java-tron 一致）
		unsettled := new(big.Int).Mul(r.Vi, big.NewInt(r.UserVotes))
		unsettled.Div(unsettled, decimalOfViReward)
		totalUnsettledSun.Add(totalUnsettledSun, unsettled)
	}
	// sun → TRX
	unsettledTRX := new(big.Float).SetInt(totalUnsettledSun)
	unsettledTRX.Quo(unsettledTRX, big.NewFloat(1e6))
	f, _ := unsettledTRX.Float64()
	return allowanceTRX + f
}
