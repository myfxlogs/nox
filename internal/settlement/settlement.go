// Package settlement 结算钩子（D-087 §3.2）。
//
// 每笔平仓有唯一、幂等、可审计的结算动作：strategy_stats / verification_stats / 淘汰统计同源更新。
// Settle 是唯一结算入口，三个调用点（手动平仓 RPC / scanAndClose / reconcile auto_close）统一替换。
package settlement

import (
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
	// DisableStrategy 禁用策略（淘汰）。
	DisableStrategy(ctx context.Context, strategy, reason string) error
	// InsertSettlement 写 position_settlements（幂等 ON CONFLICT DO NOTHING，返修包②返回 won）。
	InsertSettlement(ctx context.Context, s *pgstore.SettlementRow) (won bool, err error)
	// HasSettlement 查 position_settlements 是否已有行（幂等短路）。
	HasSettlement(ctx context.Context, posID int64) (bool, error)
	// SumCashflowsNet 返回持仓 swap+funding+commission 现金流 USD 合计（D-127 P4 返修①：net=realized+cashflowsNet）。
	SumCashflowsNet(ctx context.Context, posID int64) (float64, error)
	// D-193-A：替代 SumCashflowsNet，返回 quality 供 completeness 判定。
	SumCashflowsNetWithQuality(ctx context.Context, posID int64) (sum float64, quality string, err error)
	// D-193 审计 P0-1：查 settlement 行的 stats_status，用于短路判定。
	// 不存在行 → 返回 ("", nil)（继续走 Settle 流程）。
	// 存在行 → 返回 stats_status（applied/skipped 短路；pending/retryable 继续）。
	GetSettlementStatsStatus(ctx context.Context, posID int64) (statsStatus string, err error)
	// D-193 审计 P0-2：统计更新失败时改 stats_status=retryable，下轮重试。
	MarkSettlementStatsRetryable(ctx context.Context, posID int64) error
}

// Eliminator 淘汰判定函数类型（从 autotrade.evaluateElimination 抽出，避免环依赖）。
// D-154 S2 加 medianPnl 入参（小样本判据 median<0 AND avg<0）。
// 返回 (shouldDisable, reason)。
type Eliminator func(totalTrades int, winRate, avgPnl, medianPnl float64, disabled bool) (bool, string)

// Settle 唯一结算入口（D-087 §3.2，D-193-A 消除 fallback + 净 PNL 事实迁移）。
// 幂等短路：stats_status=applied/skipped → 直接返回（D-193 审计 P0-1）。
// 跳过规则（写行但不入样本）：realized NULL / expectedUsd≤0 / fx 缺失 / pnl_estimated。
// D-193-A：cashflows 查询失败 → 写 unknown/retryable 行 + 返回 error（消除 fallback-to-zero）。
// 通过（complete+actual）→ UpsertVerificationSample + UpdateStrategyStat + 淘汰检查；
// 统计更新失败 → MarkSettlementStatsRetryable + 返回 error（D-193 审计 P0-2）。
func SettleAt(ctx context.Context, deps Deps, posID int64, elim Eliminator, now time.Time) error {
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
		cashflowsNet, cfQuality, err = deps.SumCashflowsNetWithQuality(ctx, posID)
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
		// D-193-A：净 PNL = price_pnl + cashflows_net，quality 和 completeness 由现金流质量决定。
		pricePnl = *p.RealizedPnl
		netPnl = pricePnl + cashflowsNet
		pnlQuality = "actual"
		completeness = "complete"
		if cfQuality == "unknown" {
			pnlQuality = "unknown"
			completeness = "unknown"
		} else if cfQuality == "estimated" {
			pnlQuality = "estimated"
			completeness = "complete" // estimated 仍 complete，但统计层不消费
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
			if completeness == "unknown" {
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

	// 淘汰检查（D-154 S1：输入源从 strategy_stats 累计列改为 AggregateStrategyStats 实时聚合；
	// disabled flag 仍查 strategy_stats——启停载体不变。两调用点同源 P3。）
	// D-200：聚合改 ByEnvironment，按持仓自身 p.Environment 过滤——每个 environment 独立淘汰判定。
	if elim != nil {
		stat, err := deps.GetStrategyStat(ctx, p.Type)
		if err != nil {
			log.Printf("[settlement] get strategy stat: %v", err)
			return nil
		}
		agg, err := deps.AggregateStrategyStatsByEnvironment(ctx, p.Type, p.Environment)
		if err != nil {
			log.Printf("[settlement] aggregate strategy stats: %v", err)
			return nil
		}
		winRate := 0.0
		if agg.Total > 0 {
			winRate = float64(agg.Wins) / float64(agg.Total)
		}
		disable, reason := elim(agg.Total, winRate, agg.AvgPnl, agg.MedianPnl, stat.Disabled)
		if disable {
			if err := deps.DisableStrategy(ctx, p.Type, reason); err != nil {
				log.Printf("[settlement] disable strategy %s: %v", p.Type, err)
			} else {
				log.Printf("[settlement] STRATEGY DISABLED type=%s: %s", p.Type, reason)
			}
		}
	}
	return nil
}

// Settle 兼容包装（D-194 §3.3：生产旧入口 = SettleAt(..., time.Now())）。
func Settle(ctx context.Context, deps Deps, posID int64, elim Eliminator) error {
	return SettleAt(ctx, deps, posID, elim, time.Now())
}

// hasCashflowKinds D-193-A S7：判断该 position 是否应有现金流（SWAPARB/CARRY/FUNDING_HEDGE/PERP_FUNDING 有 swap/funding）。
// CROSS_VENUE/MEANREVERT 无 swap/funding 现金流 → no_cashflows 是正常状态，不判 unknown。
func hasCashflowKinds(posType string) bool {
	switch posType {
	case "SWAPARB", "CARRY", "FUNDING_HEDGE", "PERP_FUNDING":
		return true
	default:
		return false
	}
}
