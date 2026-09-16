// paper_trades CRUD——自 event_cases.go 拆出（D-309 纯移动，零行为变更）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// PaperTrade paper_trades 行（M1 §3）。
type PaperTrade struct {
	ID                   int64
	CaseID               int // REFERENCES event_cases(id) UNIQUE
	AlertID              *int64
	SourceType           string // hkex / edgar
	Code                 string
	Market               string
	Direction            string // buy / hold / sell
	SuggestedQty         *int
	SuggestedPrice       *float64
	ActualPrice          *float64 // nullable
	Status               string   // suggested / settled / expired
	Confidence           string   // high / medium / low
	SampleSize           *int
	Rationale            *string
	ModelNetReturnPct    *float64 // 小数 0-1
	ModelFailDrawdownPct *float64 // 小数 0-1
	ModelSuccessRate     *float64 // 小数 0-1
	HoldingPeriodDays    *int
	ActualNetReturnPct   *float64 // nullable，结算后填
	ActualOutcome        *string  // success / failed
	CreatedAt            time.Time
	SettledAt            *time.Time
}

const paperTradeCols = `id, case_id, alert_id, source_type, code, market, direction,
	suggested_qty, suggested_price, actual_price, status, confidence,
	sample_size, rationale, model_net_return_pct, model_fail_drawdown_pct,
	model_success_rate, holding_period_days, actual_net_return_pct, actual_outcome,
	created_at, settled_at`

func scanPaperTrade(sc interface{ Scan(...any) error }, r *PaperTrade) error {
	return sc.Scan(&r.ID, &r.CaseID, &r.AlertID, &r.SourceType, &r.Code, &r.Market, &r.Direction,
		&r.SuggestedQty, &r.SuggestedPrice, &r.ActualPrice, &r.Status, &r.Confidence,
		&r.SampleSize, &r.Rationale, &r.ModelNetReturnPct, &r.ModelFailDrawdownPct,
		&r.ModelSuccessRate, &r.HoldingPeriodDays, &r.ActualNetReturnPct, &r.ActualOutcome,
		&r.CreatedAt, &r.SettledAt)
}

// InsertPaperTrade 插入 paper_trades，回填 id + created_at（AcceptJudgment RPC 用，M3 §4）。
func (s *Store) InsertPaperTrade(ctx context.Context, r *PaperTrade) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO paper_trades (
			case_id, alert_id, source_type, code, market, direction,
			suggested_qty, suggested_price, actual_price, status, confidence,
			sample_size, rationale, model_net_return_pct, model_fail_drawdown_pct,
			model_success_rate, holding_period_days, actual_net_return_pct, actual_outcome
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		RETURNING id, created_at
	`, r.CaseID, r.AlertID, r.SourceType, r.Code, r.Market, r.Direction,
		r.SuggestedQty, r.SuggestedPrice, r.ActualPrice, r.Status, r.Confidence,
		r.SampleSize, r.Rationale, r.ModelNetReturnPct, r.ModelFailDrawdownPct,
		r.ModelSuccessRate, r.HoldingPeriodDays, r.ActualNetReturnPct, r.ActualOutcome,
	).Scan(&r.ID, &r.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert paper_trade: %w", err)
	}
	return nil
}

// ListPaperTrades 列出 paper_trades（status 空串=全量；按 created_at DESC）。
func (s *Store) ListPaperTrades(ctx context.Context, status string, limit int) ([]PaperTrade, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT ` + paperTradeCols + ` FROM paper_trades`
	args := []any{}
	if status != "" {
		q += ` WHERE status=$1`
		args = append(args, status)
	}
	q += ` ORDER BY created_at DESC LIMIT ` + fmt.Sprintf("%d", limit)
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("list paper_trades: %w", err)
	}
	defer rows.Close()
	var out []PaperTrade
	for rows.Next() {
		var r PaperTrade
		if err := scanPaperTrade(rows, &r); err != nil {
			return nil, fmt.Errorf("scan paper_trade: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SettlePaperTrade 结算更新：actual_outcome + actual_price + actual_net_return_pct + status=settled + settled_at（M3 §2）。
func (s *Store) SettlePaperTrade(ctx context.Context, tradeID int64, actualOutcome string, actualPrice *float64, actualNetReturnPct *float64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE paper_trades SET
			actual_outcome=$1, actual_price=$2, actual_net_return_pct=$3,
			status='settled', settled_at=NOW()
		WHERE id=$4
	`, actualOutcome, actualPrice, actualNetReturnPct, tradeID)
	if err != nil {
		return fmt.Errorf("settle paper_trade: %w", err)
	}
	return nil
}

// PaperTradeByID 按 id 取单条（SettlePaperTrade RPC 校验用）。
func (s *Store) PaperTradeByID(ctx context.Context, tradeID int64) (*PaperTrade, error) {
	r := &PaperTrade{}
	err := scanPaperTrade(s.pool.QueryRow(ctx, `SELECT `+paperTradeCols+` FROM paper_trades WHERE id=$1`, tradeID), r)
	if err != nil {
		return nil, fmt.Errorf("paper_trade by id: %w", err)
	}
	return r, nil
}

// PaperTradeByCaseID 按 case_id 查是否已建 paper_trade（AcceptJudgment 四步校验②，M3 §4）。
func (s *Store) PaperTradeByCaseID(ctx context.Context, caseID int) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM paper_trades WHERE case_id=$1)`, caseID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("paper_trade by case_id: %w", err)
	}
	return exists, nil
}
