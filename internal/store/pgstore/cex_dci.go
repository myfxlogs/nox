// cex_dci.go —— D-183：fin_cex_dci_quote 三所 DCI/双币产品报价 CRUD+清理。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// CexDCIQuote fin_cex_dci_quote 行（venue×underlying×strike×tenor×apy）。
type CexDCIQuote struct {
	ID         int64
	Venue      string
	Underlying string
	InvestCcy  string
	Strike     *float64
	OptType    string
	DeliveryTs *time.Time
	TenorDays  *int
	APY        float64
	ProductRef string
	OrderRef   string // D-191-R10：申购 orderId 参（采集面解析入库；缺行=不可申购参数）
	// D-191-R11：申购量界（NULL=venue 未给行——decimal 缺=跳次档）。
	MinAmount       *float64
	MaxAmount       *float64
	PurchaseDecimal *int
	Ts              time.Time
}

// InsertCexDCIQuote 插入 DCI 报价行，回填 id+ts。
func (s *Store) InsertCexDCIQuote(ctx context.Context, r *CexDCIQuote) error {
	err := s.pool.QueryRow(ctx, `
		INSERT INTO fin_cex_dci_quote (venue, underlying, invest_ccy, strike, opt_type, delivery_ts, tenor_days, apy, product_ref, order_ref, min_amount, max_amount, purchase_decimal)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)
		RETURNING id, ts`,
		r.Venue, r.Underlying, r.InvestCcy, r.Strike, r.OptType, r.DeliveryTs, r.TenorDays, r.APY, r.ProductRef, r.OrderRef, r.MinAmount, r.MaxAmount, r.PurchaseDecimal,
	).Scan(&r.ID, &r.Ts)
	if err != nil {
		return fmt.Errorf("insert cex dci quote: %w", err)
	}
	return nil
}

// PruneCexDCIQuote 30d 滚动清理（照 PruneCexEarnAPR 同款，janitor 日轮挂）。
func (s *Store) PruneCexDCIQuote(ctx context.Context, keepDays int) error {
	cutoff := time.Now().AddDate(0, 0, -keepDays)
	if _, err := s.pool.Exec(ctx, `DELETE FROM fin_cex_dci_quote WHERE ts < $1`, cutoff); err != nil {
		return fmt.Errorf("prune cex dci quote: %w", err)
	}
	return nil
}
