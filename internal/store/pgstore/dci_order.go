// dci_order.go —— D-191：fin_dci_order 幂等账本+执行回执面。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// DCIOrder 申购单（request_id 幂等键防重发）。
type DCIOrder struct {
	ID         int64
	RequestID  string
	Venue      string
	TemplateID string
	ProductRef string
	Underlying string
	OptType    string
	Strike     *float64
	InvestCcy  string
	Qty        float64
	TenorDays  *int
	Status     string
	Response   string
}

// CreateDCIOrder 登记 pending 单（request_id 冲突=已受理→created=false 防重）。
func (s *Store) CreateDCIOrder(ctx context.Context, o *DCIOrder) (int64, bool, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_dci_order
		  (request_id, venue, template_id, product_ref, underlying, opt_type,
		   strike, invest_ccy, qty, tenor_days)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (request_id) DO NOTHING
		RETURNING id`,
		o.RequestID, o.Venue, o.TemplateID, o.ProductRef, o.Underlying, o.OptType,
		o.Strike, o.InvestCcy, o.Qty, o.TenorDays).Scan(&id)
	if err != nil {
		return 0, false, nil // 冲突=幂等命中，非错
	}
	return id, true, nil
}

// FinishDCIOrder 收口（filled|failed|rejected+response 存证截断 2k）。
func (s *Store) FinishDCIOrder(ctx context.Context, id int64, status, response string) error {
	if len(response) > 2000 {
		response = response[:2000]
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE fin_dci_order SET status=$2, response=$3, resolved_at=now()
		WHERE id=$1 AND status='pending'`, id, status, response)
	if err != nil {
		return fmt.Errorf("finish dci order: %w", err)
	}
	return nil
}

// BestDCIQuote 报价面选档：venue×underlying×opt_type、tenor≤上限、apy 最优最新。
func (s *Store) ListDCIQuotes(ctx context.Context, venue, underlying, optType, investCcy string, maxTenorDays int) ([]CexDCIQuote, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, venue, underlying, invest_ccy, strike, opt_type, delivery_ts,
		       tenor_days, apy, product_ref, ts
		FROM fin_cex_dci_quote
		WHERE venue=$1 AND underlying=$2 AND opt_type=$3
		  AND ($4='' OR invest_ccy=$4)
		  AND (tenor_days IS NULL OR tenor_days <= $5)
		ORDER BY apy DESC, ts DESC LIMIT 50`,
		venue, underlying, optType, investCcy, maxTenorDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CexDCIQuote
	for rows.Next() {
		var q CexDCIQuote
		if err := rows.Scan(&q.ID, &q.Venue, &q.Underlying, &q.InvestCcy, &q.Strike,
			&q.OptType, &q.DeliveryTs, &q.TenorDays, &q.APY, &q.ProductRef, &q.Ts); err != nil {
			return nil, err
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// InsertPersonalPosition 回执入账（kind='other' 合规 CHECK；qty/note/price_symbol）。
func (s *Store) InsertPersonalPosition(ctx context.Context, asset, kind, priceSymbol, note string, qty *float64, openedAt *time.Time) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO personal_positions (asset, kind, price_symbol, qty, note, opened_at)
		VALUES ($1,$2,$3,$4,$5,COALESCE($6,now())) RETURNING id`,
		asset, kind, priceSymbol, qty, note, openedAt).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert personal position: %w", err)
	}
	return id, nil
}
