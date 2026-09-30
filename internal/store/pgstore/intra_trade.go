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
		WHERE (venue = $1 OR venue_key = $1) AND symbol = $2 AND ts >= $3 ORDER BY ts`, venue, symbol, since)
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

func (s *Store) scanIntraTrades(ctx context.Context, q string, args ...any) ([]IntraTrade, error) {
	rows, err := s.pool.Query(ctx, q, args...)
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

// IntraEvent intra_events 一行（D-441 S2：kill/circuit 栅栏事件）。
type IntraEvent struct {
	ID       int64
	Ts       time.Time
	Kind     string // kill / circuit
	Baseline string
	Detail   string
}

// InsertIntraEvent 栅栏事件落库（kind: kill/circuit；baseline 由 Step 上下文给，kill=空串）。
func (s *Store) InsertIntraEvent(ctx context.Context, kind, baseline, detail string) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO intra_events (kind, baseline, detail) VALUES ($1,$2,$3)`,
		kind, baseline, detail)
	if err != nil {
		return fmt.Errorf("insert intra event: %w", err)
	}
	return nil
}

// HasIntraEventToday D-441 S3：当日（UTC）同 kind 事件已存在？（引擎告警 episode 去重面）。
func (s *Store) HasIntraEventToday(ctx context.Context, kind string) (bool, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*)::int FROM intra_events WHERE kind=$1 AND ts::date = CURRENT_DATE`, kind).Scan(&n)
	if err != nil {
		return false, fmt.Errorf("has intra event today: %w", err)
	}
	return n > 0, nil
}

// ListIntraTrades D-441 S2：成交列表（id DESC LIMIT；baseline 空=全部）。
func (s *Store) ListIntraTrades(ctx context.Context, baseline string, limit int) ([]IntraTrade, error) {
	q := `SELECT id, baseline, symbol, venue, side, status, entry_ts, entry_px, qty,
		notional_usd, margin_usd, stop_px, exit_ts, exit_px, exit_reason,
		slippage_bps, pnl_usd, signal_note, trade_date
		FROM paper_intra_trades`
	args := []any{}
	if baseline != "" {
		q += ` WHERE baseline = $1`
		args = append(args, baseline)
	}
	q += fmt.Sprintf(` ORDER BY id DESC LIMIT $%d`, len(args)+1)
	args = append(args, limit)
	return s.scanIntraTrades(ctx, q, args...)
}

// IntraBaselineStatRow IntraTrackStats 聚合行（D-441 S2）。
type IntraBaselineStatRow struct {
	Baseline    string
	Total       int
	Open        int
	Closed      int
	PnlUSD      float64
	WinCount    int
	SlipP90Bps  *float64
	CircuitDays int
	KillCount   int
}

// IntraTrackStatsQuery D-441 S2：按 baseline 聚合成交+事件统计（单 SQL）。
// circuit_days=intra_events kind=circuit 去重天数；kill_count=kind=kill 计数；
// 事件 baseline 空串（kill 事件）归入全部 baseline 行（Step kill 是轨级事件非单基线）。
func (s *Store) IntraTrackStatsQuery(ctx context.Context) ([]IntraBaselineStatRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.baseline,
			COUNT(*)::int AS total,
			COUNT(*) FILTER (WHERE t.status='open')::int AS open,
			COUNT(*) FILTER (WHERE t.status='closed')::int AS closed,
			COALESCE(SUM(t.pnl_usd) FILTER (WHERE t.pnl_usd IS NOT NULL), 0) AS pnl_usd,
			COUNT(*) FILTER (WHERE t.pnl_usd > 0)::int AS win_count,
			percentile_cont(0.9) WITHIN GROUP (ORDER BY t.slippage_bps)
				FILTER (WHERE t.slippage_bps IS NOT NULL) AS slip_p90,
			(SELECT COUNT(DISTINCT ts::date) FROM intra_events
				WHERE kind='circuit' AND (baseline='' OR baseline=t.baseline))::int AS circuit_days,
			(SELECT COUNT(*) FROM intra_events
				WHERE kind='kill' AND (baseline='' OR baseline=t.baseline))::int AS kill_count
		FROM paper_intra_trades t
		GROUP BY t.baseline
		ORDER BY t.baseline`)
	if err != nil {
		return nil, fmt.Errorf("intra track stats: %w", err)
	}
	defer rows.Close()
	var out []IntraBaselineStatRow
	for rows.Next() {
		var r IntraBaselineStatRow
		if err := rows.Scan(&r.Baseline, &r.Total, &r.Open, &r.Closed, &r.PnlUSD,
			&r.WinCount, &r.SlipP90Bps, &r.CircuitDays, &r.KillCount); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListIntraVenues 日内纸面轨 venue 动态派生（业主裁定 2026-10-01，替代 IntraPairs 硬编码）：
// mt = broker_accounts.status='connected' 的 venue（账户增删即随动，1314615 死引用实证）；
// cex = exchange_configs 存在 simulated=false 行的 venue 裸名（表无 enabled/status 列——
// 行存在即已配置，simulated=false=主网行口径，同 ListVenueIsReal CEX 半边）。
func (s *Store) ListIntraVenues(ctx context.Context) (mt []string, cex []string, err error) {
	mtRows, err := s.pool.Query(ctx,
		`SELECT venue FROM broker_accounts WHERE status = 'connected' ORDER BY venue`)
	if err != nil {
		return nil, nil, fmt.Errorf("intra venues mt: %w", err)
	}
	for mtRows.Next() {
		var v string
		if err := mtRows.Scan(&v); err != nil {
			mtRows.Close()
			return nil, nil, err
		}
		mt = append(mt, v)
	}
	mtRows.Close()
	if err := mtRows.Err(); err != nil {
		return nil, nil, err
	}
	cexRows, err := s.pool.Query(ctx,
		`SELECT DISTINCT venue FROM exchange_configs WHERE NOT simulated ORDER BY venue`)
	if err != nil {
		return nil, nil, fmt.Errorf("intra venues cex: %w", err)
	}
	defer cexRows.Close()
	for cexRows.Next() {
		var v string
		if err := cexRows.Scan(&v); err != nil {
			return nil, nil, err
		}
		cex = append(cex, v)
	}
	return mt, cex, cexRows.Err()
}

// IntraDaysElapsed D-441 S5：min(entry_ts)::date 距今天数（空表=0，天数差含首日）。
func (s *Store) IntraDaysElapsed(ctx context.Context) (int, error) {
	var d *int
	err := s.pool.QueryRow(ctx,
		`SELECT (CURRENT_DATE - MIN(entry_ts)::date)::int FROM paper_intra_trades`).Scan(&d)
	if err != nil {
		return 0, fmt.Errorf("intra days elapsed: %w", err)
	}
	if d == nil {
		return 0, nil
	}
	return *d + 1, nil // 首日算第 1 天
}
