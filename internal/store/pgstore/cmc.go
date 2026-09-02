package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// D-205 CMC 采集器 Phase 1：fin_cmc_* 4 表持久化。
// 4 写入 + 3 查询方法，挂在 *Store 上复用 s.pool。

// ---------- 结构体 ----------

type CMCPrice struct {
	InstrumentID string
	BidPrice     float64
	AskPrice     float64
	MidPrice     float64
	MarketStatus string
	QuoteTime    time.Time
	FetchedAt    time.Time
}

type CMCAccountSummary struct {
	AccountID                int64
	TotalAccountValue        float64
	TotalCashBalance         float64
	TotalUnrealisedPnL       float64
	TotalEquityFreeForMargin float64
	TotalEquityUsedInMargin  float64
	CloseoutPrice            float64
	DistanceToCloseoutPct    float64
	FetchedAt                time.Time
}

type CMCPosition struct {
	AccountID          int64
	InstrumentID       string
	Direction          string
	Quantity           float64
	EntryPrice         float64
	UnrealisedPnL      float64
	MarginAmount       float64
	MarginPercentage   float64
	EvaluationBidPrice float64
	EvaluationAskPrice float64
	FetchedAt          time.Time
}

type CMCHistoryTx struct {
	AccountID       int64
	TransactionID   string
	TransactionType string
	Amount          float64
	Balance         float64
	Currency        string
	OrderID         string
	TransactionTime time.Time
	FetchedAt       time.Time
}

// ---------- 写入方法（4 个） ----------

func (s *Store) InsertCMCPrice(ctx context.Context, p CMCPrice) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO fin_cmc_prices
		   (instrument_id, bid_price, ask_price, mid_price, market_status, quote_time, fetched_at)
		 VALUES ($1,$2,$3,$4,$5,$6,COALESCE($7,now()))`,
		p.InstrumentID, p.BidPrice, p.AskPrice, p.MidPrice, p.MarketStatus, p.QuoteTime, p.FetchedAt)
	if err != nil {
		return fmt.Errorf("insert cmc price: %w", err)
	}
	return nil
}

func (s *Store) InsertCMCAccountSummary(ctx context.Context, a CMCAccountSummary) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO fin_cmc_account_summary
		   (account_id, total_account_value, total_cash_balance, total_unrealised_pnl,
		    total_equity_free_for_margin, total_equity_used_in_margin,
		    closeout_price, distance_to_closeout_pct, fetched_at)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,COALESCE($9,now()))`,
		a.AccountID, a.TotalAccountValue, a.TotalCashBalance, a.TotalUnrealisedPnL,
		a.TotalEquityFreeForMargin, a.TotalEquityUsedInMargin,
		a.CloseoutPrice, a.DistanceToCloseoutPct, a.FetchedAt)
	if err != nil {
		return fmt.Errorf("insert cmc account summary: %w", err)
	}
	return nil
}

func (s *Store) InsertCMCPositions(ctx context.Context, accountID int64, positions []CMCPosition) error {
	if len(positions) == 0 {
		return nil
	}
	rows := make([][]any, len(positions))
	for i, p := range positions {
		rows[i] = []any{
			accountID, p.InstrumentID, p.Direction, p.Quantity, p.EntryPrice,
			p.UnrealisedPnL, p.MarginAmount, p.MarginPercentage,
			p.EvaluationBidPrice, p.EvaluationAskPrice, p.FetchedAt,
		}
	}
	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"fin_cmc_positions"},
		[]string{"account_id", "instrument_id", "direction", "quantity", "entry_price",
			"unrealised_pnl", "margin_amount", "margin_percentage",
			"evaluation_bid_price", "evaluation_ask_price", "fetched_at"},
		pgx.CopyFromRows(rows))
	if err != nil {
		return fmt.Errorf("insert cmc positions: %w", err)
	}
	return nil
}

func (s *Store) UpsertCMCHistory(ctx context.Context, accountID int64, txs []CMCHistoryTx) error {
	if len(txs) == 0 {
		return nil
	}
	for _, t := range txs {
		_, err := s.pool.Exec(ctx,
			`INSERT INTO fin_cmc_history
			   (account_id, transaction_id, transaction_type, amount, balance,
			    currency, order_id, transaction_time, fetched_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,COALESCE($9,now()))
			 ON CONFLICT (transaction_id) DO NOTHING`,
			accountID, t.TransactionID, t.TransactionType, t.Amount, t.Balance,
			t.Currency, t.OrderID, t.TransactionTime, t.FetchedAt)
		if err != nil {
			return fmt.Errorf("upsert cmc history %s: %w", t.TransactionID, err)
		}
	}
	return nil
}

// ---------- 查询方法（3 个） ----------

// LastCMCHistoryTime 返回 fin_cmc_history 最近一条 transaction_time；无历史返回零值 + nil。
func (s *Store) LastCMCHistoryTime(ctx context.Context, accountID int64) (time.Time, error) {
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT transaction_time FROM fin_cmc_history
		 WHERE account_id = $1 ORDER BY transaction_time DESC LIMIT 1`, accountID).Scan(&t)
	if err == nil {
		return t, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, nil
	}
	return time.Time{}, fmt.Errorf("last cmc history time: %w", err)
}

// LastCMCPriceFetchedAt 返回 fin_cmc_prices 最近一条 fetched_at；空表返回 ErrNoRows（watchdog 依赖）。
func (s *Store) LastCMCPriceFetchedAt(ctx context.Context) (time.Time, error) {
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT fetched_at FROM fin_cmc_prices ORDER BY fetched_at DESC LIMIT 1`).Scan(&t)
	if err != nil {
		return time.Time{}, err
	}
	return t, nil
}

// LastCMCPriceMid 返回 fin_cmc_prices 指定 instrument 最近一条 mid_price；空表返回 ErrNoRows。
func (s *Store) LastCMCPriceMid(ctx context.Context, instrumentID string) (float64, error) {
	var v float64
	err := s.pool.QueryRow(ctx,
		`SELECT mid_price FROM fin_cmc_prices
		 WHERE instrument_id = $1 ORDER BY fetched_at DESC LIMIT 1`, instrumentID).Scan(&v)
	if err != nil {
		return 0, err
	}
	return v, nil
}
