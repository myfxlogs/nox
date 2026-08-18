// ledger（出入金台账，§2.8）存储。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// LedgerEntry ledger 行。Amount 正 = 入金，负 = 出金。
type LedgerEntry struct {
	ID       int64
	Date     time.Time
	Channel  string
	Currency string
	Amount   float64
	FeeRate  float64
	Tier     string
	Note     string
}

// TierSummary 按 tier 汇总（LedgerSummary RPC）。
type TierSummary struct {
	Tier       string
	Inflow     float64
	Outflow    float64
	Net        float64
	EntryCount int
}

// AddLedgerEntry 插入一条出入金记录，返回新 id。
func (s *Store) AddLedgerEntry(ctx context.Context, e *LedgerEntry) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO ledger (date, channel, currency, amount, fee_rate, tier, note)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING id
	`, e.Date, e.Channel, e.Currency, e.Amount, e.FeeRate, e.Tier, e.Note).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("add ledger entry: %w", err)
	}
	return id, nil
}

// ListLedgerEntries 按时间倒序返回流水（limit≤0 默认 100）。
func (s *Store) ListLedgerEntries(ctx context.Context, limit, offset int) ([]LedgerEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, date, channel, currency, amount, fee_rate, tier, note
		FROM ledger
		ORDER BY date DESC, id DESC
		LIMIT $1 OFFSET $2
	`, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list ledger entries: %w", err)
	}
	defer rows.Close()
	var out []LedgerEntry
	for rows.Next() {
		var e LedgerEntry
		if err := rows.Scan(&e.ID, &e.Date, &e.Channel, &e.Currency, &e.Amount, &e.FeeRate, &e.Tier, &e.Note); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LedgerSummary 按 tier 汇总出入金。
func (s *Store) LedgerSummary(ctx context.Context) ([]TierSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT tier,
		       COALESCE(SUM(amount) FILTER (WHERE amount > 0), 0) AS inflow,
		       COALESCE(SUM(-amount) FILTER (WHERE amount < 0), 0) AS outflow,
		       COALESCE(SUM(amount), 0) AS net,
		       COUNT(*) AS entry_count
		FROM ledger
		GROUP BY tier
		ORDER BY tier
	`)
	if err != nil {
		return nil, fmt.Errorf("ledger summary: %w", err)
	}
	defer rows.Close()
	var out []TierSummary
	for rows.Next() {
		var t TierSummary
		if err := rows.Scan(&t.Tier, &t.Inflow, &t.Outflow, &t.Net, &t.EntryCount); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}
