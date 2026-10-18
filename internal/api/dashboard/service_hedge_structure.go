// service_hedge_structure.go —— GetHedgeStructure 复合读面（arb service_hedge.go+
// service_hedge_carry.go+freshLive 三同名件对拍，D-212 对冲工作台）。
// 数据源：fin_trx_account（质押腿 tron_power）+ cmcLiveProvider 内存态
// （分域 >30s 回退 DB）+ fin_cmc_positions/summary/prices/history +
// position_daily_income（carry）+ fin_trx_vote_apr（透传只渲不运算）。
// 空表容忍=对应字段零值/nil（前端「数据未就绪」）；CMC 平台口径=margin×50%
// 强平/margin×80% 预警（platform_rule_50——arb 同源注释随件迁）。
package dashboard

import (
	"context"
	"encoding/json"
	"time"

	connect "connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	"nox/internal/data/collect/cmc"
	v1 "nox/internal/gen/dashboardv1"
)

// freshLive D-236 变更9：内存 LiveState 优先（cmcLiveProvider），分域新鲜度
// >30s 回退 DB——position 域（Ask/Mid/Qty/Lev/PnL 依 PosTs），summary 域
// （Cash/Equity/Margin 依 SumTs）。
func (s *Service) freshLive(ctx context.Context) cmc.LiveState {
	var ls cmc.LiveState
	if s.cmcLiveProvider != nil {
		ls = s.cmcLiveProvider()
	}
	posFresh := !ls.PosTs.IsZero() && time.Since(ls.PosTs) <= 30*time.Second
	if !posFresh || ls.Mid <= 0 {
		// D-212 mid 兜底序②：fin_cmc_prices 最新 mid 行（cmc_rest 主源——
		// WS 无 instrument 价面；IS NOT NULL 过滤天然筛 ws 零值行）。
		if mid, _, err := s.store.LastCMCPriceMidWithTime(ctx, "X-AFQUL"); err == nil && mid > 0 {
			ls.Mid = mid
		}
	}
	if !posFresh || ls.Ask <= 0 {
		if cmcPos, err := s.store.LastCMCPositionByInstrument(ctx, "X-AFQUL"); err == nil {
			if !posFresh || ls.Ask <= 0 {
				ls.Ask = cmcPos.EvaluationAskPrice
			}
			if !posFresh || ls.Mid <= 0 {
				// 兜底序③④：eval bid→eval ask（短腿 CMC 只给 ask——
				// 无 bid 时 ask 载估值如实标非伪 mid；mid=0 时 ask 已填故用 ask）。
				ls.Mid = cmcPos.EvaluationBidPrice
				if ls.Mid <= 0 {
					ls.Mid = cmcPos.EvaluationAskPrice
				}
			}
			if !posFresh || ls.Qty <= 0 {
				ls.Qty = cmcPos.Quantity
			}
			if !posFresh || ls.Lev <= 0 {
				if cmcPos.MarginPercentage > 0 {
					ls.Lev = 1.0 / cmcPos.MarginPercentage
				}
			}
			if !posFresh || ls.PnL == 0 {
				ls.PnL = cmcPos.UnrealisedPnL
			}
			if !posFresh {
				ls.PosTs = cmcPos.FetchedAt
			}
		}
	}
	sumFresh := !ls.SumTs.IsZero() && time.Since(ls.SumTs) <= 30*time.Second
	if !sumFresh {
		if cmcSum, err := s.store.LastCMCAccountSummary(ctx); err == nil {
			ls.Cash = cmcSum.TotalCashBalance
			ls.Equity = cmcSum.TotalAccountValue
			ls.Margin = cmcSum.TotalEquityUsedInMargin
			ls.SumTs = cmcSum.FetchedAt
		}
	}
	return ls
}

// computeHedgeCarry 计算 carry（近 7 日+累计+年化）——arb 同名件对拍：
// cum=(energy+voting)×mid+interest；年化时间感知（cum/days×365，firstIncomeDay
// 缺=0 诚实缺）；对冲后分段窗=[hedgeStart,now]；mtd=月窗同口径。
func (s *Service) computeHedgeCarry(ctx context.Context, mid, qtyStaked float64) *v1.HedgeCarry {
	now := time.Now().UTC()
	from7d := now.Add(-7 * 24 * time.Hour)
	energy7d, voting7d, _, _ := s.store.SumDailyIncomeByRange(ctx, &from7d, &now)
	overnight7d, _ := s.store.SumCMCOvernightByRange(ctx, &from7d, &now)
	energyAll, votingAll, interestAll, _ := s.store.SumDailyIncomeByRange(ctx, nil, nil)
	cumUsd := (energyAll+votingAll)*mid + interestAll
	var annualized float64
	if mid > 0 && qtyStaked > 0 {
		if firstDay, err := s.store.FirstDailyIncomeDay(ctx); err == nil && firstDay != nil {
			days := now.Sub(*firstDay).Hours() / 24
			if days < 1 {
				days = 1
			}
			annualized = cumUsd / (qtyStaked * mid) / days * 365 * 100
		}
	}
	hedgedCumUsd, hedgedAnnual := cumUsd, annualized
	if hedgeStart, err := s.store.FirstCMCPositionOpenedAt(ctx); err == nil && hedgeStart != nil {
		if energyH, votingH, interestH, err := s.store.SumDailyIncomeByRange(ctx, hedgeStart, &now); err == nil {
			hedgedCumUsd = (energyH+votingH)*mid + interestH
			if daysH := now.Sub(*hedgeStart).Hours() / 24; mid > 0 && qtyStaked > 0 {
				if daysH < 1 {
					daysH = 1
				}
				hedgedAnnual = hedgedCumUsd / (qtyStaked * mid) / daysH * 365 * 100
			}
		}
	}
	monthStart := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	mtdE, mtdV, mtdI, _ := s.store.SumDailyIncomeByRange(ctx, &monthStart, &now)
	mtdDays, _ := s.store.CountDailyIncomeByRange(ctx, &monthStart, &now)
	return &v1.HedgeCarry{
		TrxRental_7DTrx:     energy7d,
		TrxVote_7DTrx:       voting7d,
		CmcOvernight_7DUsd:  overnight7d,
		CumUsd:              cumUsd,
		AnnualizedPct:       annualized,
		HedgedCumUsd:        hedgedCumUsd,
		HedgedAnnualizedPct: hedgedAnnual,
		MtdNetUsd:           (mtdE+mtdV)*mid + mtdI,
		MtdSampleDays:       int32(mtdDays),
	}
}

func (s *Service) GetHedgeStructure(ctx context.Context, _ *connect.Request[v1.GetHedgeStructureRequest]) (*connect.Response[v1.GetHedgeStructureResponse], error) {
	resp := &v1.GetHedgeStructureResponse{}
	trxAcc, trxErr := s.store.LastTRXAccount(ctx)
	hasTRX := trxErr == nil
	trxApr, trxAprErr := s.store.LastTRXVoteApr(ctx)
	hasTrxApr := trxAprErr == nil
	ls := s.freshLive(ctx)
	cmcSum, cmcSumErr := s.store.LastCMCAccountSummary(ctx)
	hasCMCSum := cmcSumErr == nil
	var baseline float64
	if cmcPos, err := s.store.LastCMCPositionByInstrument(ctx, "X-AFQUL"); err == nil {
		baseline = cmcPos.EntryPrice
	}
	legSpot := &v1.HedgeLegSpot{Label: "质押现货"}
	if hasTRX {
		legSpot.QtyStaked = float64(trxAcc.TronPower) / 1e6
		legSpot.QtyLiquid = float64(trxAcc.BalanceSun) / 1e6
		legSpot.Stake = &v1.HedgeStake{
			Energy:    float64(trxAcc.EnergyDelegated) / 1e6,
			Bandwidth: float64(trxAcc.BandwidthStaked) / 1e6,
		}
	}
	if hasTrxApr {
		legSpot.VoteAprCurrent = trxApr.CurrentRate
		legSpot.VoteAprMax = trxApr.MaxRate
		legSpot.VoteAprSpread = trxApr.Spread
		if trxApr.MaxSr != nil {
			legSpot.VoteAprMaxSr = *trxApr.MaxSr
		}
		legSpot.VoteAprTs = timestamppb.New(trxApr.FetchedAt)
	}
	// D-236 变更13：现货腿浮盈=对冲镜像（−空单平台自报 PnL——镜像近似非独立估值）
	legSpot.PnlVsBase = -ls.PnL
	legShort := &v1.HedgeLegShort{Label: "CMC 空单"}
	if ls.Qty > 0 {
		legShort.Qty = ls.Qty
	}
	legShort.PnlVsBase = ls.PnL // 平台真值（业主令取 cmc 真实数据不反推）
	if ls.Lev > 0 {
		legShort.Lev = int32(ls.Lev)
	}
	if hasCMCSum {
		legShort.Liq = cmcSum.CloseoutPrice
		legShort.DistPct = cmcSum.DistanceToCloseoutPct
	}
	var netExposureTrx, netExposureUsd float64
	if hasTRX && ls.Qty > 0 {
		netExposureTrx = float64(trxAcc.BalanceSun)/1e6 + float64(trxAcc.TronPower)/1e6 - ls.Qty
		netExposureUsd = netExposureTrx * ls.Mid
	}
	carry := s.computeHedgeCarry(ctx, ls.Mid, legSpot.QtyStaked)
	accts := &v1.HedgeAccounts{}
	var updatedAt time.Time
	if hasTRX {
		accts.TrxBalance = float64(trxAcc.BalanceSun) / 1e6
		accts.TrxAllowance = float64(trxAcc.RewardNumSun) / 1e6 // D-285 tronscan rewardNum 真值
		accts.EnergyDelegated = float64(trxAcc.EnergyDelegated) / 1e6
		accts.Votes = float64(trxAcc.VotesTotal)
		accts.NetLimit = float64(trxAcc.NetLimit)
		accts.NetUsed = float64(trxAcc.NetUsed)
		accts.FreeNetLimit = float64(trxAcc.FreeNetLimit)
		accts.FreeNetUsed = float64(trxAcc.FreeNetUsed)
		accts.EnergyLimit = float64(trxAcc.EnergyLimit)
		accts.EnergyUsed = float64(trxAcc.EnergyUsed)
		accts.BandwidthStatus = bandwidthStatus(trxAcc)
		updatedAt = trxAcc.FetchedAt
	}
	if ls.Cash > 0 || ls.Equity > 0 {
		accts.CmcCash = ls.Cash
		accts.CmcEquity = ls.Equity
		if ls.SumTs.After(updatedAt) {
			updatedAt = ls.SumTs
		}
	} else if hasCMCSum {
		accts.CmcCash = cmcSum.TotalCashBalance
		accts.CmcEquity = cmcSum.TotalAccountValue
		if cmcSum.FetchedAt.After(updatedAt) {
			updatedAt = cmcSum.FetchedAt
		}
	}
	if !updatedAt.IsZero() {
		accts.UpdatedAt = timestamppb.New(updatedAt)
	}
	// D-225 口径：平台强平净值=margin×50%、预警=margin×80%（CMC 面板标定）
	margin := ls.Margin
	if margin <= 0 && hasCMCSum {
		margin = cmcSum.TotalEquityUsedInMargin
	}
	var alerts json.RawMessage
	if hasCMCSum {
		alerts = cmcSum.Alerts
	}
	resp.Structure = &v1.HedgeStructure{
		BaselinePrice:      baseline,
		Mid:                ls.Mid,
		Ask:                ls.Ask,
		LegSpot:            legSpot,
		LegShort:           legShort,
		NetExposureTrx:     netExposureTrx,
		NetExposureUsd:     netExposureUsd,
		Carry:              carry,
		Accts:              accts,
		CloseoutSource:     "platform_rule_50",
		PlatformAlertState: cmc.AlertsToState(alerts),
		PlatformAlerts:     string(alerts),
		CloseoutLevelUsd:   margin * 0.5,
		WarningLevelUsd:    margin * 0.8,
	}
	if s.cmcWsHealth != nil {
		resp.Structure.WsConnected = s.cmcWsHealth()
	}
	return connect.NewResponse(resp), nil
}
