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

// LastCMCPriceMidWithTime 返回 fin_cmc_prices 指定 instrument 最近一条 mid_price + fetched_at（D-208 v2：新鲜度判定用）。
func (s *Store) LastCMCPriceMidWithTime(ctx context.Context, instrumentID string) (float64, time.Time, error) {
	var v float64
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT mid_price, fetched_at FROM fin_cmc_prices
		 WHERE instrument_id = $1 ORDER BY fetched_at DESC LIMIT 1`, instrumentID).Scan(&v, &t)
	if err != nil {
		return 0, time.Time{}, err
	}
	return v, t, nil
}

// LastCMCAccountSummary 返回最近一条 CMC 账户摘要（含 closeout_price/distance_to_closeout_pct）；空表返回 ErrNoRows。
func (s *Store) LastCMCAccountSummary(ctx context.Context) (CMCAccountSummary, error) {
	var a CMCAccountSummary
	err := s.pool.QueryRow(ctx,
		`SELECT account_id, total_account_value, total_cash_balance, total_unrealised_pnl,
		        total_equity_free_for_margin, total_equity_used_in_margin,
		        closeout_price, distance_to_closeout_pct, fetched_at
		 FROM fin_cmc_account_summary ORDER BY fetched_at DESC LIMIT 1`).Scan(
		&a.AccountID, &a.TotalAccountValue, &a.TotalCashBalance, &a.TotalUnrealisedPnL,
		&a.TotalEquityFreeForMargin, &a.TotalEquityUsedInMargin,
		&a.CloseoutPrice, &a.DistanceToCloseoutPct, &a.FetchedAt)
	if err != nil {
		return CMCAccountSummary{}, err
	}
	return a, nil
}

// LastCMCPositionByInstrument 返回最近一条该 instrument 的 CMC 持仓（含 unrealised_pnl/quantity/entry_price/opened_at/order_id）；空表返回 ErrNoRows。
func (s *Store) LastCMCPositionByInstrument(ctx context.Context, instrumentID string) (CMCPosition, error) {
	var p CMCPosition
	err := s.pool.QueryRow(ctx,
		`SELECT account_id, instrument_id, direction, quantity, entry_price,
		        unrealised_pnl, margin_amount, margin_percentage,
		        evaluation_bid_price, evaluation_ask_price, opened_at, order_id, fetched_at
		 FROM fin_cmc_positions WHERE instrument_id = $1 ORDER BY fetched_at DESC LIMIT 1`, instrumentID).Scan(
		&p.AccountID, &p.InstrumentID, &p.Direction, &p.Quantity, &p.EntryPrice,
		&p.UnrealisedPnL, &p.MarginAmount, &p.MarginPercentage,
		&p.EvaluationBidPrice, &p.EvaluationAskPrice, &p.OpenedAt, &p.OrderID, &p.FetchedAt)
	if err != nil {
		return CMCPosition{}, err
	}
	return p, nil
}

// ListCMCHistory 返回最近 limit 条 CMC 历史流水（倒序，D-208 v2-C：前端流水小表用）。
func (s *Store) ListCMCHistory(ctx context.Context, accountID int64, limit int) ([]CMCHistoryTx, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.pool.Query(ctx,
		`SELECT account_id, transaction_id, transaction_type, amount, balance,
		        currency, order_id, transaction_time, fetched_at
		 FROM fin_cmc_history WHERE account_id = $1
		 ORDER BY transaction_time DESC LIMIT $2`, accountID, limit)
	if err != nil {
		return nil, fmt.Errorf("list cmc history: %w", err)
	}
	defer rows.Close()
	var out []CMCHistoryTx
	for rows.Next() {
		var t CMCHistoryTx
		if err := rows.Scan(&t.AccountID, &t.TransactionID, &t.TransactionType, &t.Amount,
			&t.Balance, &t.Currency, &t.OrderID, &t.TransactionTime, &t.FetchedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// ---------- CMCSource 适配器（D-208 v2：*Store 实现 position.CMCSource 接口） ----------
//
// CMCSource 接口在 internal/position/price.go 定义，*Store 通过以下 4 方法实现。
// main 装配：persMon.SetCMCSource(st) / dashSvc.SetCMCSource(st)。
// nil 安全：调用方判断 cmcSource != nil 才走 CMC 路径。

// CMCSourceAdapter 包装 *Store 实现 position.CMCSource 接口（避免 *Store 直接依赖 context 之外的接口）。
// 用 ctx 字段让接口方法无需传 ctx（PersonalMonitor 的 onQuote 已有 ctx 但 CMCSource 接口设计为无 ctx）。
type CMCSourceAdapter struct {
	Store *Store
	Ctx   context.Context // 调用方传入的 ctx（如 persMon 的 onQuote ctx）
}

// LastCMCMid 返回 instrumentID 的 mid + fetched_at（新鲜度判定用）。
func (a *CMCSourceAdapter) LastCMCMid(instrumentID string) (float64, time.Time, error) {
	return a.Store.LastCMCPriceMidWithTime(a.Ctx, instrumentID)
}

// LastCMCCloseout 返回 closeout 强平价 + distance_to_closeout_pct（CMC 百分数）。
func (a *CMCSourceAdapter) LastCMCCloseout() (float64, float64, error) {
	summary, err := a.Store.LastCMCAccountSummary(a.Ctx)
	if err != nil {
		return 0, 0, err
	}
	return summary.CloseoutPrice, summary.DistanceToCloseoutPct, nil
}

// LastCMCUnrealised 返回 instrumentID 的 unrealised_pnl（broker 权威值）。
func (a *CMCSourceAdapter) LastCMCUnrealised(instrumentID string) (float64, error) {
	pos, err := a.Store.LastCMCPositionByInstrument(a.Ctx, instrumentID)
	if err != nil {
		return 0, err
	}
	return pos.UnrealisedPnL, nil
}

// LastCMCPosition 返回 instrumentID 的持仓详情（entry_price/quantity/opened_at/order_id/leverage 推导）。
func (a *CMCSourceAdapter) LastCMCPosition(instrumentID string) (CMCPosition, error) {
	return a.Store.LastCMCPositionByInstrument(a.Ctx, instrumentID)
}
