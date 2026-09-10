// D-212 对冲工作台：GetHedgeStructure + ListHedgeFlow RPC handler（SSOT §1/§2）。
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"arb/internal/collect/cmc"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// GetHedgeStructure 组装对冲结构卡（SSOT §1）：双腿+净敞口真实计算+carry+双账户摘要。
// D-236 变更9：CMC 字段全改 freshLive（内存优先，>30s 分域回退 DB）；PnL 取平台自报值（type7 帧直通）；
// 镜像恒和=0 退役（D-212 旧设计，业主真值原则取代）；净敞口 USD 分母改流 Mid。
// 数据源：fin_trx_account（质押腿 18070）+ freshLive（空单腿+mid+summary）+
// fin_cmc_account_summary（强平价 DB 兜底）+ position_daily_income（carry）+ fin_cmc_history（隔夜息）。
// 空表容忍：任一源空 → 对应字段零值/nil（前端显示「数据未就绪」）。
func (s *Service) GetHedgeStructure(ctx context.Context, req *connect.Request[v1.GetHedgeStructureRequest]) (*connect.Response[v1.GetHedgeStructureResponse], error) {
	resp := &v1.GetHedgeStructureResponse{}

	// TRX 账户快照（质押腿数量源=tron_power，v1.2 裁定修正）
	trxAcc, trxErr := s.store.LastTRXAccount(ctx)
	hasTRX := trxErr == nil

	// D-231 S3：TRX 投票收益率（fin_trx_vote_apr 最新行透传，只渲染不运算）
	trxApr, trxAprErr := s.store.LastTRXVoteApr(ctx)
	hasTrxApr := trxAprErr == nil

	// D-236 变更9：CMC 字段全走 freshLive（内存优先，分域 >30s 回退 DB）
	ls := s.freshLive(ctx)

	// CMC 账户摘要（closeout_price=强平价，distance_to_closeout_pct=距强平%；DB 兜底+alerts）
	cmcSum, cmcSumErr := s.store.LastCMCAccountSummary(ctx)
	hasCMCSum := cmcSumErr == nil

	// baseline=空单开仓价（DB positions 行 entry_price；现货购价未知，注释标注）
	var baseline float64
	if cmcPos, err := s.store.LastCMCPositionByInstrument(ctx, "X-AFQUL"); err == nil {
		baseline = cmcPos.EntryPrice
	}

	// 质押腿（leg_spot）：qty_staked=votes_total（展示），qty_liquid=balance
	// D-236 变更13：PnL=对冲镜像（−空单平台自报 PnL，后端算零信任）；现货购价未知，镜像近似非独立估值
	legSpot := &v1.HedgeLegSpot{Label: "质押现货"}
	if hasTRX {
		qtyStaked := float64(trxAcc.TronPower) / 1e6
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
	// D-236 变更13：现货腿浮盈=对冲镜像（−空单平台自报 PnL）。现货购价未知，镜像近似非现货独立估值。
	legSpot.PnlVsBase = -ls.PnL

	// 空单腿（leg_short）：D-236 变更9 PnL=平台自报值（LiveState.PnL，type7 帧直通 unrealisedPnLInPrimaryCurrency）
	legShort := &v1.HedgeLegShort{Label: "CMC 空单"}
	if ls.Qty > 0 {
		legShort.Qty = ls.Qty
	}
	legShort.PnlVsBase = ls.PnL // 平台真值（业主令：取 cmc 真实数据，不再反推计算）
	if ls.Lev > 0 {
		legShort.Lev = int32(ls.Lev)
	}
	// BrokerUnrealised 停用不删（proto 兼容，D-236 变更7 注释标注停用）
	if hasCMCSum {
		legShort.Liq = cmcSum.CloseoutPrice
		legShort.DistPct = cmcSum.DistanceToCloseoutPct
	}

	// 净敞口（v1.2 真实计算：(balance+staked−short_qty)×mid）
	// D-236 变更9：分母改流 Mid（LiveState.Mid，内存优先）
	var netExposureTrx, netExposureUsd float64
	if hasTRX && ls.Qty > 0 {
		balance := float64(trxAcc.BalanceSun) / 1e6
		staked := float64(trxAcc.TronPower) / 1e6
		netExposureTrx = balance + staked - ls.Qty
		netExposureUsd = netExposureTrx * ls.Mid
	}

	// carry（近 7 日 + 累计 + 年化）——mid 用流值
	carry := s.computeHedgeCarry(ctx, ls.Mid, legSpot.QtyStaked)

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
	// D-236 变更9：CMC 账户摘要用 freshLive 值（内存优先）
	if ls.Cash > 0 || ls.Equity > 0 {
		accts.CmcCash = ls.Cash
		accts.CmcEquity = ls.Equity
		if ls.SumTs.After(updatedAt) {
			updatedAt = ls.SumTs
		}
	} else if hasCMCSum {
		// freshLive summary 域全零（未接线+DB 也空）→ 用 DB 兜底
		accts.CmcCash = cmcSum.TotalCashBalance
		accts.CmcEquity = cmcSum.TotalAccountValue
		if cmcSum.FetchedAt.After(updatedAt) {
			updatedAt = cmcSum.FetchedAt
		}
	}
	if !updatedAt.IsZero() {
		accts.UpdatedAt = timestamppb.New(updatedAt)
	}

	// D-236 变更9：净值/距强平随 Margin 流值派生（D-225 口径不变：margin×50%/80%）
	var closeoutLevelUSD, warningLevelUSD float64
	margin := ls.Margin
	if margin <= 0 && hasCMCSum {
		margin = cmcSum.TotalEquityUsedInMargin // DB 兜底
	}
	closeoutLevelUSD = margin * 0.5
	warningLevelUSD = margin * 0.8
	var alerts json.RawMessage
	if hasCMCSum {
		alerts = cmcSum.Alerts
	}
	resp.Structure = &v1.HedgeStructure{
		BaselinePrice:      baseline,
		Mid:                ls.Mid, // D-236 变更9：流 Mid（内存优先）
		Ask:                ls.Ask, // D-236 变更9：流 Ask（内存优先）
		LegSpot:            legSpot,
		LegShort:           legShort,
		NetExposureTrx:     netExposureTrx,
		NetExposureUsd:     netExposureUsd,
		Carry:              carry,
		Accts:              accts,
		CloseoutSource:     "platform_rule_50", // D-225 v2.2：CMC 面板标定为保证金 50%
		PlatformAlertState: cmc.AlertsToState(alerts),
		PlatformAlerts:     string(alerts),
		CloseoutLevelUsd:   closeoutLevelUSD,
		WarningLevelUsd:    warningLevelUSD,
	}
	// D-193-A/R9：CMC ws 事件驱动健康（nil=未接线→false=黄点 REST 兜底）
	if s.cmcWsHealth != nil {
		resp.Structure.WsConnected = s.cmcWsHealth()
	}
	return connect.NewResponse(resp), nil
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
