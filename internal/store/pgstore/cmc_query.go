// CMC 查询方法（从 cmc.go 拆出，保 cmc.go ≤300 行）。
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

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

// LastCMCPriceStatusAndFetchedAt 返回 fin_cmc_prices 最近一条 src + market_status + fetched_at
// （v1.2 R'-1：watchdog×门控互作修复——休市 status=closed 时跳过 stale 判定；
// D-225 v2.1：加 src 返回——watchdog src 感知阈值 cmc_ws=10min / rest=5min）。
// 空表返回 ErrNoRows（与 LastCMCPriceFetchedAt 行为一致）。
func (s *Store) LastCMCPriceStatusAndFetchedAt(ctx context.Context) (string, string, time.Time, error) {
	var src, status string
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT src, market_status, fetched_at FROM fin_cmc_prices ORDER BY fetched_at DESC LIMIT 1`).Scan(&src, &status, &t)
	if err != nil {
		return "", "", time.Time{}, err
	}
	return src, status, t, nil
}

// LastCMCPriceMidWithTime 返回 fin_cmc_prices 指定 instrument 最近一条 mid_price + fetched_at（D-208 v2：新鲜度判定用）。
// D-225 v2.1：加 mid_price IS NOT NULL 过滤——WS 行 bid/mid 为 NULL，不污染 mid 查询（D-208 个人监控线保护）。
func (s *Store) LastCMCPriceMidWithTime(ctx context.Context, instrumentID string) (float64, time.Time, error) {
	var v float64
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT mid_price, fetched_at FROM fin_cmc_prices
		 WHERE instrument_id = $1 AND mid_price IS NOT NULL
		 ORDER BY fetched_at DESC LIMIT 1`, instrumentID).Scan(&v, &t)
	if err != nil {
		return 0, time.Time{}, err
	}
	return v, t, nil
}

// LastCMCPriceForSide 返回指定 instrument + 方向的平仓价（D-225 v2.1）。
// short(Sell) → ask_price：WS 行(src=cmc_ws) ask 优先，无 WS 行回退 REST 行 ask。
// 返回 (px, fetchedAt, src, error)；空表返回 ErrNoRows。
func (s *Store) LastCMCPriceForSide(ctx context.Context, instrumentID, side string) (float64, time.Time, string, error) {
	// short → ask；其余 → ask（当前只有 X-AFQUL 空单，统一取 ask）
	// WS 行优先
	var px float64
	var t time.Time
	var src string
	err := s.pool.QueryRow(ctx,
		`SELECT ask_price, fetched_at, src FROM fin_cmc_prices
		 WHERE instrument_id = $1 AND src = 'cmc_ws' AND ask_price > 0
		 ORDER BY fetched_at DESC LIMIT 1`, instrumentID).Scan(&px, &t, &src)
	if err == nil {
		return px, t, src, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, time.Time{}, "", fmt.Errorf("last cmc price forside ws: %w", err)
	}
	// 回退 REST 行 ask
	err = s.pool.QueryRow(ctx,
		`SELECT ask_price, fetched_at, src FROM fin_cmc_prices
		 WHERE instrument_id = $1 AND ask_price > 0
		 ORDER BY fetched_at DESC LIMIT 1`, instrumentID).Scan(&px, &t, &src)
	if err != nil {
		return 0, time.Time{}, "", err
	}
	return px, t, src, nil
}

// LastCMCAlerts 返回 fin_cmc_account_summary 最近一行 alerts 原文 JSON（D-225 v2.1）。
func (s *Store) LastCMCAlerts(ctx context.Context) (json.RawMessage, error) {
	var alerts json.RawMessage
	err := s.pool.QueryRow(ctx,
		`SELECT alerts FROM fin_cmc_account_summary ORDER BY fetched_at DESC LIMIT 1`).Scan(&alerts)
	if err != nil {
		return nil, err
	}
	return alerts, nil
}

// LastCMCMarketStatus 返回最近一条非空 market_status + fetched_at（D-225 v2.1：S5 接线用）。
func (s *Store) LastCMCMarketStatus(ctx context.Context) (string, time.Time, error) {
	var status string
	var t time.Time
	err := s.pool.QueryRow(ctx,
		`SELECT market_status, fetched_at FROM fin_cmc_prices
		 WHERE market_status IS NOT NULL AND market_status != ''
		 ORDER BY fetched_at DESC LIMIT 1`).Scan(&status, &t)
	if err != nil {
		return "", time.Time{}, err
	}
	return status, t, nil
}

// LastCMCAccountSummary 返回最近一条 CMC 账户摘要（含 closeout_price/distance_to_closeout_pct/alerts）；空表返回 ErrNoRows。
func (s *Store) LastCMCAccountSummary(ctx context.Context) (CMCAccountSummary, error) {
	var a CMCAccountSummary
	err := s.pool.QueryRow(ctx,
		`SELECT account_id, total_account_value, total_cash_balance, total_unrealised_pnl,
		        total_equity_free_for_margin, total_equity_used_in_margin,
		        closeout_price, distance_to_closeout_pct, fetched_at, alerts
		 FROM fin_cmc_account_summary ORDER BY fetched_at DESC LIMIT 1`).Scan(
		&a.AccountID, &a.TotalAccountValue, &a.TotalCashBalance, &a.TotalUnrealisedPnL,
		&a.TotalEquityFreeForMargin, &a.TotalEquityUsedInMargin,
		&a.CloseoutPrice, &a.DistanceToCloseoutPct, &a.FetchedAt, &a.Alerts)
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

// FirstCMCPositionOpenedAt D-236 变更15：对冲起点=fin_cmc_positions 最早 opened_at。
// 空表 → nil（对冲后视图回落=结构累计同值并注明）。
func (s *Store) FirstCMCPositionOpenedAt(ctx context.Context) (*time.Time, error) {
	var day *time.Time
	err := s.pool.QueryRow(ctx, `SELECT MIN(opened_at) FROM fin_cmc_positions`).Scan(&day)
	return day, err
}
