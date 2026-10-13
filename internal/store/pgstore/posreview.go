package pgstore

import (
	"context"
	"time"
)

// PositionReview position_reviews 行（D-266 M4.5 判决书）：
// 数字列纯 Go 直填——无证据=NULL 不落伪值；review_note/followup_action=LLM 面。
type PositionReview struct {
	PositionID       int64
	HypothesisID     *string // 出仓机会的 opportunity_id（假设源引）
	ExpectedNetUSD   *float64
	ActualNetUSD     *float64
	ExpectedCostUSD  *float64
	ActualCostUSD    *float64
	CostDeviationUSD *float64
	ExpectedRiskUSD  *float64
	ActualRiskUSD    *float64
	RiskDeviationUSD *float64
	FollowupAction   *string // continue|reduce|quarantine|retire（仅建议，不翻牌）
	ReviewNote       *string
	ReviewedAt       time.Time
}

// ClosedForReview CLOSED∧无 review 行的仓投影（判决书输入面）。
type ClosedForReviewRow struct {
	ID             int64
	OpportunityID  string
	Type           string
	ExpectedNetBPS *float64
	NotionalUSD    *float64
	ExitKind       *string
	FeesUSD        *float64
	FeeQuality     *string
	OpenedAt       *time.Time
	ClosedAt       *time.Time
}

// ListClosedWithoutReview 判决书缺口集：status='CLOSED' ∧ 无 position_reviews 行。
// posChange kick 后调——幂等，已评仓自动退出集合。
func (s *Store) ListClosedWithoutReview(ctx context.Context) ([]ClosedForReviewRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT p.id, p.opportunity_id, p.type,
		p.expected_net_bps, p.notional_usd, p.exit_kind, p.fees_usd, p.fee_quality,
		p.opened_at, p.closed_at
		FROM positions p
		LEFT JOIN position_reviews r ON r.position_id = p.id
		WHERE p.status = 'CLOSED' AND r.position_id IS NULL
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClosedForReviewRow
	for rows.Next() {
		var r ClosedForReviewRow
		if err := rows.Scan(&r.ID, &r.OpportunityID, &r.Type,
			&r.ExpectedNetBPS, &r.NotionalUSD, &r.ExitKind, &r.FeesUSD, &r.FeeQuality,
			&r.OpenedAt, &r.ClosedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpsertPositionReview 判决书幂等写（position_id PK 覆盖——LLM 重试/复评安全）。
func (s *Store) UpsertPositionReview(ctx context.Context, r *PositionReview) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO position_reviews
		(position_id, hypothesis_id, expected_net_usd, actual_net_usd,
		 expected_cost_usd, actual_cost_usd, cost_deviation_usd,
		 expected_risk_usd, actual_risk_usd, risk_deviation_usd,
		 followup_action, review_note, reviewed_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (position_id) DO UPDATE SET
		 hypothesis_id=EXCLUDED.hypothesis_id, expected_net_usd=EXCLUDED.expected_net_usd,
		 actual_net_usd=EXCLUDED.actual_net_usd, expected_cost_usd=EXCLUDED.expected_cost_usd,
		 actual_cost_usd=EXCLUDED.actual_cost_usd, cost_deviation_usd=EXCLUDED.cost_deviation_usd,
		 expected_risk_usd=EXCLUDED.expected_risk_usd, actual_risk_usd=EXCLUDED.actual_risk_usd,
		 risk_deviation_usd=EXCLUDED.risk_deviation_usd, followup_action=EXCLUDED.followup_action,
		 review_note=EXCLUDED.review_note, reviewed_at=EXCLUDED.reviewed_at`,
		r.PositionID, r.HypothesisID, r.ExpectedNetUSD, r.ActualNetUSD,
		r.ExpectedCostUSD, r.ActualCostUSD, r.CostDeviationUSD,
		r.ExpectedRiskUSD, r.ActualRiskUSD, r.RiskDeviationUSD,
		r.FollowupAction, r.ReviewNote, r.ReviewedAt)
	return err
}
