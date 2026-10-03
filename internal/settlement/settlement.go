// Package settlement 结算钩子（D-087 §3.2）。
//
// 每笔平仓有唯一、幂等、可审计的结算动作：strategy_stats / verification_stats / 淘汰统计同源更新。
// Settle 是唯一结算入口，三个调用点（手动平仓 RPC / scanAndClose / reconcile auto_close）统一替换。
package settlement

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/verification"
)

// Deps 结算依赖（注入面，调用方装配）。
type Deps interface {
	// GetPosition 读单条持仓（含 typed 字段）。
	GetPosition(ctx context.Context, id int64) (*pgstore.Position, error)
	// ListPositionLegs 读持仓腿。
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	// ContractSizeByCanonical 查 CS（已含 VenueAccount 归一）。
	ContractSizeByCanonical(ctx context.Context, venue, canonical string) (float64, bool, error)
	// USDPer 查计价币→USD 汇率（FXRateProvider）。
	USDPer(ctx context.Context, quoteCcy string, at time.Time) (float64, bool)
	// UpsertVerificationSample 增量验证统计。
	UpsertVerificationSample(ctx context.Context, vkey string, expectedUsd, realizedUsd float64, closedAt time.Time) error
	// UpdateStrategyStat 策略统计增量。
	UpdateStrategyStat(ctx context.Context, strategy string, realizedPnl float64) error
	// GetStrategyStat 读策略统计（D-154 起仅查 disabled/disabled_reason；累计列不再用于淘汰判定）。
	GetStrategyStat(ctx context.Context, strategy string) (*pgstore.StrategyStat, error)
	// AggregateStrategyStats 实时聚合策略全量平仓净值（D-154 S1 淘汰判定 v2 真相源）。
	AggregateStrategyStats(ctx context.Context, strategy string) (pgstore.AggregateStat, error)
	// AggregateStrategyStatsByEnvironment D-200：按 environment 过滤的聚合（environment='' 等价于原行为）。
	AggregateStrategyStatsByEnvironment(ctx context.Context, strategy, environment string) (pgstore.AggregateStat, error)
	// AggregateStrategyStatsByEnvironmentFamily D-256-A S2：风险族聚合（多策略合并样本，p.type=ANY($1)）。
	// CARRY/SWAPARB 同族合并统计，避免分桶样本量不足永不触发淘汰。
	AggregateStrategyStatsByEnvironmentFamily(ctx context.Context, strategies []string, environment string) (pgstore.AggregateStat, error)
	// DisableStrategy 禁用策略（淘汰）。
	DisableStrategy(ctx context.Context, strategy, reason string) error
	// InsertAlert 写告警（D-268 告警点 4：结算时策略淘汰告警）。
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
	// InsertSettlement 写 position_settlements（幂等 ON CONFLICT DO NOTHING，返修包②返回 won）。
	InsertSettlement(ctx context.Context, s *pgstore.SettlementRow) (won bool, err error)
	// HasSettlement 查 position_settlements 是否已有行（幂等短路）。
	HasSettlement(ctx context.Context, posID int64) (bool, error)
	// SumCashflowsNet 返回持仓 swap+funding+commission 现金流 USD 合计（D-127 P4 返修①：net=realized+cashflowsNet）。
	SumCashflowsNet(ctx context.Context, posID int64) (float64, error)
	// D-193-A：替代 SumCashflowsNet，返回 quality 供 completeness 判定。
	SumCashflowsNetWithQuality(ctx context.Context, posID int64) (sum float64, quality string, err error)
	// D-438：结算专用净额——funding 实收以 income_events 为准替换公式投影轨。
	SumCashflowsNetForSettle(ctx context.Context, posID int64, hasFundingLeg, fundingDue bool) (sum float64, quality string, err error)
	// D-193 审计 P0-1：查 settlement 行的 stats_status，用于短路判定。
	// 不存在行 → 返回 ("", nil)（继续走 Settle 流程）。
	// 存在行 → 返回 stats_status（applied/skipped 短路；pending/retryable 继续）。
	GetSettlementStatsStatus(ctx context.Context, posID int64) (statsStatus string, err error)
	// D-193 审计 P0-2：统计更新失败时改 stats_status=retryable，下轮重试。
	MarkSettlementStatsRetryable(ctx context.Context, posID int64) error
	// D-475a S7：预测-实测对照——warn flag 自动写+同键查重+纸面行回查。
	InsertFlag(ctx context.Context, f *pgstore.VenueSymbolFlag) error
	ListActiveFlags(ctx context.Context) (map[string]pgstore.VenueSymbolFlag, error)
	// LatestPaperByLegs canonical+两腿裸名回查最近纸面行（venue 组合串含两腿裸名匹配）。
	LatestPaperByLegs(ctx context.Context, canonical string, legVenues []string) (*pgstore.PaperSpotCarry, error)
}

// Eliminator 淘汰判定函数类型（从 autotrade.evaluateElimination 抽出，避免环依赖）。
// D-154 S2 加 medianPnl 入参（小样本判据 median<0 AND avg<0）。
// 返回 (shouldDisable, reason)。
type Eliminator func(totalTrades int, winRate, avgPnl, medianPnl float64, disabled bool) (bool, string)

// FamilyResolver D-256-A S2：策略→风险族映射（避免环依赖，autotrade.riskFamily 注入）。
// 返回同族策略名列表（如 CARRY→[CARRY,SWAPARB]），用于族级聚合。
// nil = 无族合并（每策略独立聚合，向后兼容）。
type FamilyResolver func(strategy string) []string

// Settle 唯一结算入口（D-087 §3.2，D-193-A 消除 fallback + 净 PNL 事实迁移）。
// 幂等短路：stats_status=applied/skipped → 直接返回（D-193 审计 P0-1）。
// 跳过规则（写行但不入样本）：realized NULL / expectedUsd≤0 / fx 缺失 / pnl_estimated。
// D-193-A：cashflows 查询失败 → 写 unknown/retryable 行 + 返回 error（消除 fallback-to-zero）。
// 通过（complete+actual）→ UpsertVerificationSample + UpdateStrategyStat + 淘汰检查；
// 统计更新失败 → MarkSettlementStatsRetryable + 返回 error（D-193 审计 P0-2）。
// D-256-A S2：family 非 nil 时淘汰聚合改用族级（CARRY/SWAPARB 合并样本）。
func SettleAt(ctx context.Context, deps Deps, posID int64, elim Eliminator, family FamilyResolver, now time.Time) error {
	// D-193 审计 P0-1：短路逻辑改为检查 stats_status。
	// applied/skipped → 已完成，短路返回 nil。
	// pending/retryable/""（无行或 legacy）→ 继续走 Settle 流程。
	statsStatus, err := deps.GetSettlementStatsStatus(ctx, posID)
	if err != nil {
		return fmt.Errorf("settle: check existing stats status: %w", err)
	}
	if statsStatus == "applied" || statsStatus == "skipped" {
		return nil // 已完成，幂等短路
	}

	// 1. 读 position
	p, err := deps.GetPosition(ctx, posID)
	if err != nil {
		return fmt.Errorf("settle: get position %d: %w", posID, err)
	}
	if p.Status != "CLOSED" {
		return fmt.Errorf("settle: position %d not CLOSED (status=%s)", posID, p.Status)
	}

	// 2. 派生 vkey
	vkey := verification.VkeyFromOppID(p.OpportunityID)

	// 3. 取腿算 notional + holdDays
	legs, err := deps.ListPositionLegs(ctx, posID)
	if err != nil {
		return fmt.Errorf("settle: list legs: %w", err)
	}
	canonical := canonicalFromOppID(p.OpportunityID)
	notionalUsd, fxSkipped := computeNotionalUSD(ctx, deps, legs, canonical)
	holdDays := computeHoldDays(p, legs)

	// 4. 算 expectedUsd（D-087 §3.4 typed 新签名：netBps + swapBpsDaily 按字段取值）
	netBps := p.ExpectedNetBps
	swapBpsDaily := 0.0
	if p.ExpectedSwapBpsDaily != nil {
		swapBpsDaily = *p.ExpectedSwapBpsDaily
	}
	expectedUsd := verification.ExpectedUsd(p.Type, netBps, swapBpsDaily, notionalUsd, holdDays)

	// 5. 跳过规则
	skippedReason := ""
	switch {
	case p.RealizedPnl == nil:
		skippedReason = "realized_pnl is NULL"
	case fxSkipped:
		skippedReason = "fx: quote ccy unavailable"
	case expectedUsd <= 0:
		skippedReason = "expected_usd <= 0"
	case strings.Contains(p.Note, "pnl_estimated"): // D-159 W3a R4：estimated 不入正式统计
		skippedReason = "pnl_estimated: partial or no calibration"
	}

	// 6. D-193-A：cashflows 查询 + 净 PNL + completeness 判定（在 InsertSettlement 前完成，
	//    因 row 需写入 net_realized_usd/pnl_quality/completeness_status/stats_status）。
	var (
		cashflowsNet   float64
		cfQuality      string
		pnlQuality     string
		completeness   string
		rowStatsStatus string
		netPnl         float64
		pricePnl       float64
	)
	if skippedReason == "" {
		// D-193-A：消除 fallback-to-zero。查询失败 → settlement 进入 unknown/retryable，不写 complete，不更新统计。
		// D-438：funding 腿判据=FILLED OPEN 的 %:perp 腿（funding 经济上仅存于 perp 合约；
		// 不复用 swapledger.isFundingLeg——其对非 XASSET 类型忽略 venue 直取 isFundingType，
		// 会漏非 funding 型仓持 perp 腿实收的场景）。
		hasFundingLeg := false
		for _, l := range legs {
			if l.Phase == "OPEN" && l.Status == "FILLED" && keydomain.IsType(l.Venue, "perp") {
				hasFundingLeg = true
				break
			}
		}
		fundingDue := holdDays >= 0.5 // ≥12h 必跨≥1个 funding 窗（OKX 00/08/16 UTC，窗距8h）
		cashflowsNet, cfQuality, err = deps.SumCashflowsNetForSettle(ctx, posID, hasFundingLeg, fundingDue)
		if err != nil {
			mv := 1
			errRow := &pgstore.SettlementRow{
				PositionID:             posID,
				Vkey:                   vkey,
				ExpectedUsd:            expectedUsd,
				SkippedReason:          "cashflow query failed: " + err.Error(),
				SettledAt:              now,
				PnlQuality:             "unknown",
				CompletenessStatus:     "unknown",
				StatsStatus:            "retryable",
				SettlementModelVersion: &mv,
				SettlementEventKey:     fmt.Sprintf("settle:%d", posID),
			}
			if p.RealizedPnl != nil {
				errRow.PricePnlUsd = p.RealizedPnl
				errRow.RealizedUsd = *p.RealizedPnl
			}
			_, _ = deps.InsertSettlement(ctx, errRow) // best-effort 写入，错误已返回
			return fmt.Errorf("settle: cashflow query pos=%d: %w", posID, err)
		}
		// D-013 ②：net_pnl = positions.realized_pnl——exec 侧经 pgstore.PositionNetUSD
		// 单源写入（价差+funding/swap+commission），结算只传播同一值，不再二次加现金流。
		// cashflowsNet 仅作组件列与 quality/completeness 判据；pricePnl 组件=净额−现金流。
		// D-013 R1 ②：EXTERNAL/CEX FILLED 平仓腿 fee NULL → partial（MT 恒显式 0 行不算缺；retryable 可升级）。
		hasPartialFeeLeg := false
		for _, l := range legs {
			if l.Phase == "CLOSE" && l.FeeUSD == nil &&
				(l.Status == "EXTERNAL" || (l.Status == "FILLED" && !keydomain.IsMTKey(l.Venue))) {
				hasPartialFeeLeg = true
				break
			}
		}
		netPnl = *p.RealizedPnl
		pricePnl = netPnl - cashflowsNet
		pnlQuality, completeness = "actual", "complete"
		if hasPartialFeeLeg {
			completeness = "partial"
		}
		if cfQuality == "unknown" {
			pnlQuality = "unknown"
			completeness = "unknown"
		} else if cfQuality == "estimated" {
			pnlQuality = "estimated" // estimated 不入正式统计（stats 层不消费）
		} else if cfQuality == "no_cashflows" && hasCashflowKinds(p.Type) {
			// D-193 审计 P1-3：传 p.Type 不是 posID
			// 应有现金流但查不到 → unknown
			pnlQuality = "unknown"
			completeness = "unknown"
		}
		// D-193 审计 P0-2：stats_status 在 InsertSettlement 时直接写入正确值，不用二次 UPDATE。
		// complete+actual → stats_status='applied'；非 complete/actual → 'skipped' 或 'retryable'。
		rowStatsStatus = "applied"
		if completeness != "complete" || pnlQuality != "actual" {
			if completeness == "unknown" || completeness == "partial" {
				rowStatsStatus = "retryable"
			} else {
				rowStatsStatus = "skipped"
			}
		}
	} else {
		// skipped 行：completeness=skipped，pnlQuality=unknown，stats_status=skipped
		pnlQuality = "unknown"
		completeness = "skipped"
		rowStatsStatus = "skipped"
		if p.RealizedPnl != nil {
			pricePnl = *p.RealizedPnl
		}
	}

	// 7. 写 position_settlements（含 D-193-A 净 PNL + 完整性字段；ON CONFLICT DO NOTHING 防并发）
	mv := 1
	row := &pgstore.SettlementRow{
		PositionID:             posID,
		Vkey:                   vkey,
		ExpectedUsd:            expectedUsd,
		HoldDays:               holdDays,
		SkippedReason:          skippedReason,
		SettledAt:              now,
		PnlQuality:             pnlQuality,
		CompletenessStatus:     completeness,
		StatsStatus:            rowStatsStatus, // D-193 审计 P0-2：直接写最终值，无二次 UPDATE
		SettlementModelVersion: &mv,
		SettlementEventKey:     fmt.Sprintf("settle:%d", posID),
	}
	if skippedReason == "" {
		row.NotionalUsd = &notionalUsd // 非跳过行写真值
		row.PricePnlUsd = &pricePnl
		row.CashflowsNetUsd = &cashflowsNet
		row.NetRealizedUsd = &netPnl
	} // 跳过行 NotionalUsd=nil → INSERT 写 NULL
	if p.RealizedPnl != nil {
		row.RealizedUsd = *p.RealizedPnl // 兼容旧查询
	}
	won, err := deps.InsertSettlement(ctx, row)
	if err != nil {
		return fmt.Errorf("settle: insert settlement: %w", err)
	}
	if !won {
		log.Printf("[settlement] pos=%d concurrent loser (settlement already exists), skipping stat update", posID)
		return nil
	}

	// D-032 ①：结算行已落账——30d 账本回撤线等 post-settle 风控检查（可选钩子，
	// 行可见于本条查询连接后触发；并发输家路径不触发，赢家侧会带）。
	if h, ok := deps.(PostSettler); ok {
		h.PostSettled(ctx, posID)
	}

	// 8. 非 complete/actual → 不入样本不入统计，直接返回
	if rowStatsStatus != "applied" {
		if skippedReason != "" {
			log.Printf("[settlement] pos=%d skipped: %s", posID, skippedReason)
		} else {
			log.Printf("[settlement] pos=%d not complete/actual: completeness=%s quality=%s stats=%s",
				posID, completeness, pnlQuality, rowStatsStatus)
		}
		return nil
	}

	// 9. 通过（complete+actual）→ UpsertVerificationSample + UpdateStrategyStat + 淘汰
	// D-193-A：netPnl = price_pnl + cashflows_net（净值一次合成，无双计）。
	closedAt := now
	if p.ClosedAt != nil {
		closedAt = *p.ClosedAt
	}
	if err := deps.UpsertVerificationSample(ctx, vkey, expectedUsd, netPnl, closedAt); err != nil {
		// D-193 审计 P0-2：统计更新失败 → 改 stats_status 为 retryable，下轮重试。
		_ = deps.MarkSettlementStatsRetryable(ctx, posID)
		return fmt.Errorf("settle: upsert verification pos=%d: %w", posID, err)
	}
	if err := deps.UpdateStrategyStat(ctx, p.Type, netPnl); err != nil {
		_ = deps.MarkSettlementStatsRetryable(ctx, posID)
		return fmt.Errorf("settle: update strategy stat pos=%d: %w", posID, err)
	}

	// 淘汰检查（D-261 S5：提取至 settlement_elim.go runElimination）。
	runElimination(ctx, deps, p, elim, family)
	// D-475a S7：XASSET_CARRY 预测-实测对照（观测面非钱闸——失败只 log 不阻塞）。
	checkXassetPredictVsActual(ctx, deps, p, posID, cashflowsNet)
	return nil
}
