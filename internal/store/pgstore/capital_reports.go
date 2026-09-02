package pgstore

import (
	"context"
	"fmt"
	"time"

	"arb/internal/capital"
)

// PositionReview D-193 §6 S3：结构化偏差事实（pgstore 层类型，对应 position_reviews 表）。
type PositionReview struct {
	PositionID       int64
	HypothesisID     *string
	ExpectedNetUSD   *float64
	ActualNetUSD     *float64
	ExpectedCostUSD  *float64
	ActualCostUSD    *float64
	CostDeviationUSD *float64
	ExpectedRiskUSD  *float64
	ActualRiskUSD    *float64
	RiskDeviationUSD *float64
	FollowupAction   *string // continue/reduce/quarantine/retire
	ReviewNote       *string
	ReviewedAt       time.Time
}

// UpsertPositionReview 创建或更新持仓复盘。
func (s *Store) UpsertPositionReview(ctx context.Context, r *PositionReview) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO position_reviews
		   (position_id, hypothesis_id, expected_net_usd, actual_net_usd,
		    expected_cost_usd, actual_cost_usd, cost_deviation_usd,
		    expected_risk_usd, actual_risk_usd, risk_deviation_usd,
		    followup_action, review_note, reviewed_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,COALESCE($13,now()))
		 ON CONFLICT (position_id) DO UPDATE SET
		   hypothesis_id = EXCLUDED.hypothesis_id,
		   expected_net_usd = EXCLUDED.expected_net_usd,
		   actual_net_usd = EXCLUDED.actual_net_usd,
		   expected_cost_usd = EXCLUDED.expected_cost_usd,
		   actual_cost_usd = EXCLUDED.actual_cost_usd,
		   cost_deviation_usd = EXCLUDED.cost_deviation_usd,
		   expected_risk_usd = EXCLUDED.expected_risk_usd,
		   actual_risk_usd = EXCLUDED.actual_risk_usd,
		   risk_deviation_usd = EXCLUDED.risk_deviation_usd,
		   followup_action = EXCLUDED.followup_action,
		   review_note = EXCLUDED.review_note,
		   reviewed_at = EXCLUDED.reviewed_at`,
		r.PositionID, r.HypothesisID, r.ExpectedNetUSD, r.ActualNetUSD,
		r.ExpectedCostUSD, r.ActualCostUSD, r.CostDeviationUSD,
		r.ExpectedRiskUSD, r.ActualRiskUSD, r.RiskDeviationUSD,
		r.FollowupAction, r.ReviewNote, r.ReviewedAt)
	if err != nil {
		return fmt.Errorf("upsert position review pos=%d: %w", r.PositionID, err)
	}
	return nil
}

// AccountPerformanceReport 账户绩效报表。
type AccountPerformanceReport struct {
	AccountID      int64
	AccountKey     string
	PrincipalUSD   float64
	CurrentEquity  *float64
	HWM            *float64
	DrawdownPct    *float64
	NetRealizedUSD *float64 // Σ net_realized_usd from settlements（四重 AND 过滤）
	TWR            *float64
	AnnualizedPct  *float64
	Quality        string // actual/estimated/unknown
}

// StrategyPerformanceReport 策略绩效报表。
type StrategyPerformanceReport struct {
	Strategy       string
	NetRealizedUSD *float64
	TotalTrades    int
	WinRate        *float64
	Quality        string
}

// DecisionChain D-193 审计 P1-13：反查链（decision → position → legs → cashflows → settlement）。
type DecisionChain struct {
	Decision   *capital.CapitalDecision
	Position   *Position
	Legs       []PositionLeg
	Cashflows  []Cashflow
	Settlement *SettlementRow
}

// GetAccountPerformance D-193 §6 S5：账户绩效报表。
// NetRealizedUSD = Σ position_settlements.net_realized_usd WHERE pnl_quality='actual' AND completeness_status='complete' AND stats_status='applied' AND net_realized_usd IS NOT NULL（四重 AND）。
// Quality = 全部 actual → 'actual'；含 unknown → 'unknown'；含 estimated（无 unknown）→ 'estimated'。
func (s *Store) GetAccountPerformance(ctx context.Context, accountID int64) (*AccountPerformanceReport, error) {
	// 账户身份
	var accountKey string
	var principalUSD float64
	err := s.pool.QueryRow(ctx,
		`SELECT account_key, principal_usd FROM capital_accounts WHERE account_id = $1`,
		accountID).Scan(&accountKey, &principalUSD)
	if err != nil {
		return nil, fmt.Errorf("get account performance account=%d: %w", accountID, err)
	}
	rpt := &AccountPerformanceReport{AccountID: accountID, AccountKey: accountKey, PrincipalUSD: principalUSD, Quality: "unknown"}

	// 当前快照（跳过 unknown）
	var currentEquity *float64
	var snapQuality *string
	err = s.pool.QueryRow(ctx,
		`SELECT liquidatable_equity_usd, quality FROM capital_snapshots
		 WHERE scope_type = 'account' AND account_id = $1
		   AND quality IN ('actual','estimated')
		   AND liquidatable_equity_usd IS NOT NULL
		 ORDER BY source_ts DESC NULLS LAST, snapshot_id DESC LIMIT 1`,
		accountID).Scan(&currentEquity, &snapQuality)
	if err == nil {
		rpt.CurrentEquity = currentEquity
		if snapQuality != nil {
			rpt.Quality = *snapQuality
		}
	}

	// HWM
	var hwm *float64
	err = s.pool.QueryRow(ctx,
		`SELECT max(liquidatable_equity_usd) FROM capital_snapshots
		 WHERE scope_type = 'account' AND account_id = $1
		   AND quality IN ('actual','estimated')
		   AND liquidatable_equity_usd IS NOT NULL`,
		accountID).Scan(&hwm)
	if err == nil && hwm != nil {
		rpt.HWM = hwm
		if currentEquity != nil && *hwm > 0 {
			dd := 1 - *currentEquity / *hwm
			rpt.DrawdownPct = &dd
		}
	}

	// NetRealizedUSD（四重 AND 过滤，D-193 审计 P1-11）
	// 通过 positions.opportunity_id 关联 capital_decisions 的 account_id（D-193-C 首版：用 position.account_id 直接关联）
	// 当前 positions 表无 account_id 列，用 capital_decisions.opportunity_id 关联
	var netRealized *float64
	var hasUnknown, hasEstimated, hasActual bool
	err = s.pool.QueryRow(ctx,
		`SELECT COALESCE(sum(ps.net_realized_usd), 0),
		        bool_or(ps.pnl_quality = 'unknown'),
		        bool_or(ps.pnl_quality = 'estimated'),
		        bool_or(ps.pnl_quality = 'actual')
		 FROM position_settlements ps
		 JOIN capital_decisions cd ON cd.opportunity_id = (SELECT opportunity_id FROM positions WHERE id = ps.position_id)
		 WHERE cd.account_id = $1
		   AND ps.pnl_quality = 'actual'
		   AND ps.completeness_status = 'complete'
		   AND ps.stats_status = 'applied'
		   AND ps.net_realized_usd IS NOT NULL`,
		accountID).Scan(&netRealized, &hasUnknown, &hasEstimated, &hasActual)
	if err == nil {
		if netRealized != nil && *netRealized != 0 {
			rpt.NetRealizedUSD = netRealized
		}
		// Quality 归约（设计 §3.6）：含 unknown → unknown；含 estimated（无 unknown）→ estimated；全 actual → actual
		if hasUnknown {
			rpt.Quality = "unknown"
		} else if hasEstimated {
			rpt.Quality = "estimated"
		} else if hasActual {
			rpt.Quality = "actual"
		}
	}
	return rpt, nil
}

// GetOverallPerformance D-193 §6 S5：总体绩效报表。
func (s *Store) GetOverallPerformance(ctx context.Context, accountIDs []int64) (*AccountPerformanceReport, error) {
	rpt := &AccountPerformanceReport{Quality: "unknown"}
	// overall 快照
	var currentEquity *float64
	var snapQuality *string
	err := s.pool.QueryRow(ctx,
		`SELECT liquidatable_equity_usd, quality FROM capital_snapshots
		 WHERE scope_type = 'overall'
		   AND quality IN ('actual','estimated')
		   AND liquidatable_equity_usd IS NOT NULL
		 ORDER BY source_ts DESC NULLS LAST, snapshot_id DESC LIMIT 1`).Scan(&currentEquity, &snapQuality)
	if err == nil {
		rpt.CurrentEquity = currentEquity
		if snapQuality != nil {
			rpt.Quality = *snapQuality
		}
	}
	// HWM
	var hwm *float64
	err = s.pool.QueryRow(ctx,
		`SELECT max(liquidatable_equity_usd) FROM capital_snapshots
		 WHERE scope_type = 'overall'
		   AND quality IN ('actual','estimated')
		   AND liquidatable_equity_usd IS NOT NULL`).Scan(&hwm)
	if err == nil && hwm != nil {
		rpt.HWM = hwm
		if currentEquity != nil && *hwm > 0 {
			dd := 1 - *currentEquity / *hwm
			rpt.DrawdownPct = &dd
		}
	}
	return rpt, nil
}

// GetStrategyPerformance D-193 §6 S5：策略绩效报表。
func (s *Store) GetStrategyPerformance(ctx context.Context, strategy string) (*StrategyPerformanceReport, error) {
	rpt := &StrategyPerformanceReport{Strategy: strategy, Quality: "unknown"}
	// 通过 positions.opportunity_id 前缀匹配 strategy（opportunity_id 格式 strategy:canonical:venueBuy:venueSell）
	var netRealized *float64
	var totalTrades int
	var winCount int
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(sum(ps.net_realized_usd), 0), count(*), count(*) FILTER (WHERE ps.net_realized_usd > 0)
		 FROM position_settlements ps
		 JOIN positions p ON p.id = ps.position_id
		 WHERE p.opportunity_id LIKE $1 || '%'
		   AND ps.pnl_quality = 'actual'
		   AND ps.completeness_status = 'complete'
		   AND ps.stats_status = 'applied'
		   AND ps.net_realized_usd IS NOT NULL`,
		strategy).Scan(&netRealized, &totalTrades, &winCount)
	if err != nil {
		return nil, fmt.Errorf("get strategy performance %s: %w", strategy, err)
	}
	if netRealized != nil && *netRealized != 0 {
		rpt.NetRealizedUSD = netRealized
	}
	rpt.TotalTrades = totalTrades
	if totalTrades > 0 {
		wr := float64(winCount) / float64(totalTrades)
		rpt.WinRate = &wr
	}
	if totalTrades > 0 {
		rpt.Quality = "actual"
	}
	return rpt, nil
}
