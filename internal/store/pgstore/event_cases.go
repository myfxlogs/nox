// D-274 事件驱动研判引擎：event_cases + paper_trades CRUD（M1 §2-§3 + M2/M3 调用面）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// EventCase event_cases 行（M1 §2）。
type EventCase struct {
	ID               int64
	AlertID          *int64 // UNIQUE，nullable（人工建库无 alert_id）
	Code             string
	Market           string
	EventType        string
	AnnounceDate     time.Time
	OfferPrice       *float64 // NUMERIC，nullable
	Currency         *string  // HKD/USD，nullable
	PrePrice         *float64 // nullable
	LotSize          *int     // nullable
	LotSizeSource    *string  // longbridge / history / unknown
	PremiumPct       *float64 // 百分比数字（非小数）
	OfferType        *string
	Proration        *string
	Conditions       *string // JSON 数组字符串
	AcquirerStrength *string
	RiskSignal       *string // red / yellow / green
	Score            *int    // 0-8
	SuggestedQty     *int
	Outcome          string // success / failed / withdrawn / pending
	SettleDate       *time.Time
	SettlePrice      *float64
	FailDrawdownPct  *float64 // 小数 0-1
	HoldingDays      *int
	SourceURL        *string
	ModelValidation  *string
	CreatedAt        time.Time
}

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

const eventCaseCols = `id, alert_id, code, market, event_type, announce_date,
	offer_price, currency, pre_price, lot_size, lot_size_source,
	premium_pct, offer_type, proration, conditions, acquirer_strength,
	risk_signal, score, suggested_qty, outcome,
	settle_date, settle_price, fail_drawdown_pct, holding_days,
	source_url, model_validation, created_at`

const paperTradeCols = `id, case_id, alert_id, source_type, code, market, direction,
	suggested_qty, suggested_price, actual_price, status, confidence,
	sample_size, rationale, model_net_return_pct, model_fail_drawdown_pct,
	model_success_rate, holding_period_days, actual_net_return_pct, actual_outcome,
	created_at, settled_at`

func scanEventCase(sc interface{ Scan(...any) error }, r *EventCase) error {
	return sc.Scan(&r.ID, &r.AlertID, &r.Code, &r.Market, &r.EventType, &r.AnnounceDate,
		&r.OfferPrice, &r.Currency, &r.PrePrice, &r.LotSize, &r.LotSizeSource,
		&r.PremiumPct, &r.OfferType, &r.Proration, &r.Conditions, &r.AcquirerStrength,
		&r.RiskSignal, &r.Score, &r.SuggestedQty, &r.Outcome,
		&r.SettleDate, &r.SettlePrice, &r.FailDrawdownPct, &r.HoldingDays,
		&r.SourceURL, &r.ModelValidation, &r.CreatedAt)
}

func scanPaperTrade(sc interface{ Scan(...any) error }, r *PaperTrade) error {
	return sc.Scan(&r.ID, &r.CaseID, &r.AlertID, &r.SourceType, &r.Code, &r.Market, &r.Direction,
		&r.SuggestedQty, &r.SuggestedPrice, &r.ActualPrice, &r.Status, &r.Confidence,
		&r.SampleSize, &r.Rationale, &r.ModelNetReturnPct, &r.ModelFailDrawdownPct,
		&r.ModelSuccessRate, &r.HoldingPeriodDays, &r.ActualNetReturnPct, &r.ActualOutcome,
		&r.CreatedAt, &r.SettledAt)
}

// InsertEventCase 插入 event_cases，回填 id + created_at。
func (s *Store) InsertEventCase(ctx context.Context, r *EventCase) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO event_cases (
			alert_id, code, market, event_type, announce_date,
			offer_price, currency, pre_price, lot_size, lot_size_source,
			premium_pct, offer_type, proration, conditions, acquirer_strength,
			risk_signal, score, suggested_qty, outcome,
			settle_date, settle_price, fail_drawdown_pct, holding_days,
			source_url, model_validation
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25)
		RETURNING id, created_at
	`, r.AlertID, r.Code, r.Market, r.EventType, r.AnnounceDate,
		r.OfferPrice, r.Currency, r.PrePrice, r.LotSize, r.LotSizeSource,
		r.PremiumPct, r.OfferType, r.Proration, r.Conditions, r.AcquirerStrength,
		r.RiskSignal, r.Score, r.SuggestedQty, r.Outcome,
		r.SettleDate, r.SettlePrice, r.FailDrawdownPct, r.HoldingDays,
		r.SourceURL, r.ModelValidation,
	).Scan(&r.ID, &r.CreatedAt)
	if err != nil {
		return fmt.Errorf("insert event_case: %w", err)
	}
	return nil
}

// EventCaseByAlertID 查同 alert_id 是否已建 case（去重预检②，M2 §1）。
func (s *Store) EventCaseByAlertID(ctx context.Context, alertID int64) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM event_cases WHERE alert_id=$1)`, alertID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("event_case by alert_id: %w", err)
	}
	return exists, nil
}

// EventCaseBySourceURL 查同 source_url 是否已建 case（去重预检①，M2 §1）。
func (s *Store) EventCaseBySourceURL(ctx context.Context, sourceURL string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM event_cases WHERE source_url=$1)`, sourceURL).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("event_case by source_url: %w", err)
	}
	return exists, nil
}

// LotSizeByCode 查同 code 历史最新 lot_size（fetchLotSize 降级链 history 层，M2 §5）。
// 返回 (lot_size, found)；found=false 表示无历史或 lot_size 为 NULL/0。
func (s *Store) LotSizeByCode(ctx context.Context, code string) (int, bool, error) {
	var lotSize *int
	err := s.pool.QueryRow(ctx, `
		SELECT lot_size FROM event_cases
		WHERE code=$1 AND lot_size IS NOT NULL AND lot_size > 0
		ORDER BY created_at DESC LIMIT 1
	`, code).Scan(&lotSize)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return 0, false, nil
		}
		return 0, false, fmt.Errorf("lot_size by code: %w", err)
	}
	if lotSize == nil {
		return 0, false, nil
	}
	return *lotSize, true, nil
}

// OfferPriceByCaseID 通过 case_id JOIN event_cases 取 offer_price（M3 §2 结算公式 success 未填默认值）。
func (s *Store) OfferPriceByCaseID(ctx context.Context, caseID int) (float64, error) {
	var offerPrice float64
	err := s.pool.QueryRow(ctx, `SELECT offer_price FROM event_cases WHERE id=$1`, caseID).Scan(&offerPrice)
	if err != nil {
		return 0, fmt.Errorf("offer_price by case_id: %w", err)
	}
	return offerPrice, nil
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

// ListEventCases 列出 event_cases（按 created_at DESC）。
func (s *Store) ListEventCases(ctx context.Context, limit int) ([]EventCase, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `SELECT `+eventCaseCols+` FROM event_cases ORDER BY created_at DESC LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("list event_cases: %w", err)
	}
	defer rows.Close()
	var out []EventCase
	for rows.Next() {
		var r EventCase
		if err := scanEventCase(rows, &r); err != nil {
			return nil, fmt.Errorf("scan event_case: %w", err)
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

// EventCaseByID 按 id 取单条（AcceptJudgment RPC 校验用）。
func (s *Store) EventCaseByID(ctx context.Context, caseID int) (*EventCase, error) {
	r := &EventCase{}
	err := scanEventCase(s.pool.QueryRow(ctx, `SELECT `+eventCaseCols+` FROM event_cases WHERE id=$1`, caseID), r)
	if err != nil {
		return nil, fmt.Errorf("event_case by id: %w", err)
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

// UpdateEventCaseOutcome 更新 event_cases 的 outcome/settle_date/settle_price/fail_drawdown_pct（建库回填+结算后更新用）。
func (s *Store) UpdateEventCaseOutcome(ctx context.Context, caseID int, outcome string, settleDate *time.Time, settlePrice *float64, failDrawdownPct *float64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE event_cases SET
			outcome=$1, settle_date=$2, settle_price=$3, fail_drawdown_pct=$4
		WHERE id=$5
	`, outcome, settleDate, settlePrice, failDrawdownPct, caseID)
	if err != nil {
		return fmt.Errorf("update event_case outcome: %w", err)
	}
	return nil
}

// GetCaseStats 全库统计（M2 §4；v1.2 裁 10：n=全库样本量不分层）。
// SuccessRate = count(outcome='success') / count(outcome IN ('success','failed'))。
// AvgDrawdown = avg(fail_drawdown_pct) WHERE outcome='failed' AND fail_drawdown_pct IS NOT NULL。
// SampleSize = count(*)（全库，含 pending）。
func (s *Store) GetCaseStats(ctx context.Context) (successRate, avgDrawdown float64, sampleSize int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT
			COUNT(*)::int AS total,
			COALESCE(COUNT(*) FILTER (WHERE outcome='success')::float8
				/ NULLIF(COUNT(*) FILTER (WHERE outcome IN ('success','failed')), 0), 0) AS success_rate,
			COALESCE(AVG(fail_drawdown_pct) FILTER (WHERE outcome='failed' AND fail_drawdown_pct IS NOT NULL), 0) AS avg_drawdown
		FROM event_cases
	`).Scan(&sampleSize, &successRate, &avgDrawdown)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("get case stats: %w", err)
	}
	return successRate, avgDrawdown, sampleSize, nil
}
