package pgstore

import (
	"context"
	"encoding/json"
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
	Src          string // D-225 v2.1: cmc_ws / cmc_rest（默认 cmc_rest）
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
	Alerts                   json.RawMessage // D-225 v2.1: accountValueAlerts 原文 JSON（空数组=正常）
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
	OpenedAt           *time.Time // D-208 v2-A：orders[0].submittedTimestamp
	OrderID            string     // D-208 v2-A：orders[0].orderId
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
	src := p.Src
	if src == "" {
		src = "cmc_rest"
	}
	// D-225 v2.1：WS 行 bid/mid=0 → NULL（NULLIF）——LastCMCPriceMidWithTime 的 IS NOT NULL 过滤才生效
	_, err := s.pool.Exec(ctx,
		`INSERT INTO fin_cmc_prices
		   (instrument_id, bid_price, ask_price, mid_price, market_status, quote_time, fetched_at, src)
		 VALUES ($1,NULLIF($2,0.0),NULLIF($3,0.0),NULLIF($4,0.0),$5,$6,COALESCE($7,now()),$8)`,
		p.InstrumentID, p.BidPrice, p.AskPrice, p.MidPrice, p.MarketStatus, p.QuoteTime, p.FetchedAt, src)
	if err != nil {
		return fmt.Errorf("insert cmc price: %w", err)
	}
	return nil
}

func (s *Store) InsertCMCAccountSummary(ctx context.Context, a CMCAccountSummary) error {
	alerts := a.Alerts
	if len(alerts) == 0 {
		alerts = json.RawMessage("[]")
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO fin_cmc_account_summary
		   (account_id, total_account_value, total_cash_balance, total_unrealised_pnl,
		    total_equity_free_for_margin, total_equity_used_in_margin,
		    closeout_price, distance_to_closeout_pct, fetched_at, alerts)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,COALESCE($9,now()),$10)`,
		a.AccountID, a.TotalAccountValue, a.TotalCashBalance, a.TotalUnrealisedPnL,
		a.TotalEquityFreeForMargin, a.TotalEquityUsedInMargin,
		a.CloseoutPrice, a.DistanceToCloseoutPct, a.FetchedAt, alerts)
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
			p.EvaluationBidPrice, p.EvaluationAskPrice, p.OpenedAt, p.OrderID, p.FetchedAt,
		}
	}
	_, err := s.pool.CopyFrom(ctx,
		pgx.Identifier{"fin_cmc_positions"},
		[]string{"account_id", "instrument_id", "direction", "quantity", "entry_price",
			"unrealised_pnl", "margin_amount", "margin_percentage",
			"evaluation_bid_price", "evaluation_ask_price", "opened_at", "order_id", "fetched_at"},
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

// ---------- 查询方法 + CMCSource 适配器见 cmc_query.go（从 cmc.go 拆出保 ≤300 行） ----------
