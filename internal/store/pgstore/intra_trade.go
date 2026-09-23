// intra_trade.go —— D-425 S1/S2：日内单品高风险轨纸面表存取（独立沙箱轨，与 carry 面隔离）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// IntraTrade paper_intra_trades 一行（开仓→出场全观测）。
type IntraTrade struct {
	ID          int64
	Baseline    string // momentum_break / meanrev
	Symbol      string
	Venue       string
	Side        string // long / short
	Status      string // open / closed
	EntryTs     time.Time
	EntryPx     float64
	Qty         float64
	NotionalUSD float64
	MarginUSD   float64
	StopPx      float64
	ExitTs      *time.Time
	ExitPx      *float64
	ExitReason  string // stop / signal / eod / circuit / kill / ''（open）
	SlippageBps *float64
	PnlUSD      *float64
	SignalNote  string
	TradeDate   time.Time
}

// InsertIntraTrade 开仓落账（信号/进场/止损价全观测），回填 id。
func (s *Store) InsertIntraTrade(ctx context.Context, r *IntraTrade) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO paper_intra_trades (baseline, symbol, venue, side, entry_ts, entry_px, qty,
			notional_usd, margin_usd, stop_px, signal_note, trade_date)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$5::timestamptz AT TIME ZONE 'UTC')
		RETURNING id`,
		r.Baseline, r.Symbol, r.Venue, r.Side, r.EntryTs, r.EntryPx, r.Qty,
		r.NotionalUSD, r.MarginUSD, r.StopPx, r.SignalNote).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("insert intra trade: %w", err)
	}
	return id, nil
}

// CloseIntraTrade 出场落账（exit_reason: stop/signal/eod/circuit/kill；滑点+盈亏全观测）。
func (s *Store) CloseIntraTrade(ctx context.Context, id int64, exitPx float64, reason string, slippageBps, pnl float64) error {
	ct, err := s.pool.Exec(ctx, `
		UPDATE paper_intra_trades SET status = 'closed', exit_ts = now(), exit_px = $2,
			exit_reason = $3, slippage_bps = $4, pnl_usd = $5
		WHERE id = $1 AND status = 'open'`, id, exitPx, reason, slippageBps, pnl)
	if err != nil {
		return fmt.Errorf("close intra trade %d: %w", id, err)
	}
	if ct.RowsAffected() == 0 {
		return fmt.Errorf("close intra trade %d: not open", id)
	}
	return nil
}

// ListOpenIntraTrades 全部在开仓（并发闸与止损管理读面；并发=1 下通常 ≤1 行）。
func (s *Store) ListOpenIntraTrades(ctx context.Context) ([]IntraTrade, error) {
	return s.scanIntraTrades(ctx, `SELECT `+intraCols+` FROM paper_intra_trades WHERE status = 'open' ORDER BY id`)
}

// IntraDayRealizedPnl 当日（UTC）已实现盈亏合计（日内熔断计账面）。
func (s *Store) IntraDayRealizedPnl(ctx context.Context, day time.Time) (float64, error) {
	var pnl float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(pnl_usd), 0) FROM paper_intra_trades
		WHERE status = 'closed' AND trade_date = $1`, day.Format("2006-01-02")).Scan(&pnl)
	if err != nil {
		return 0, fmt.Errorf("intra day realized pnl: %w", err)
	}
	return pnl, nil
}

// TicksWindow ticks 窗口读面（信号计算与回放门数据源；ts 升序）。
func (s *Store) TicksWindow(ctx context.Context, venue, symbol string, since time.Time) ([]TickRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT ts, venue, symbol, bid, ask, venue_type FROM ticks
		WHERE venue = $1 AND symbol = $2 AND ts >= $3 ORDER BY ts`, venue, symbol, since)
	if err != nil {
		return nil, fmt.Errorf("ticks window: %w", err)
	}
	defer rows.Close()
	var out []TickRow
	for rows.Next() {
		var r TickRow
		if err := rows.Scan(&r.Ts, &r.Venue, &r.Symbol, &r.Bid, &r.Ask, &r.VenueType); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

const intraCols = `id, baseline, symbol, venue, side, status, entry_ts, entry_px, qty,
	notional_usd, margin_usd, stop_px, exit_ts, exit_px, exit_reason, slippage_bps, pnl_usd,
	signal_note, trade_date`

func (s *Store) scanIntraTrades(ctx context.Context, q string) ([]IntraTrade, error) {
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []IntraTrade
	for rows.Next() {
		var r IntraTrade
		if err := rows.Scan(&r.ID, &r.Baseline, &r.Symbol, &r.Venue, &r.Side, &r.Status,
			&r.EntryTs, &r.EntryPx, &r.Qty, &r.NotionalUSD, &r.MarginUSD, &r.StopPx,
			&r.ExitTs, &r.ExitPx, &r.ExitReason, &r.SlippageBps, &r.PnlUSD,
			&r.SignalNote, &r.TradeDate); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
