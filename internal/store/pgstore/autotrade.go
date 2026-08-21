package pgstore

import (
	"context"
	"fmt"
	"time"
)

// AutotradeLog 对应 autotrade_logs 表（D-070）。
type AutotradeLog struct {
	ID            int64
	Ts            time.Time
	OpportunityID string
	PositionID    *int64
	Strategy      string
	Canonical     string
	VenueBuy      string
	VenueSell     string
	Qty           float64
	Status        string // EXECUTED / FAILED / REJECTED
	Reason        string
	NetBps        float64
}

// InsertAutotradeLog 记录一笔自动交易（成败均记）。
func (s *Store) InsertAutotradeLog(ctx context.Context, l *AutotradeLog) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO autotrade_logs (opportunity_id, position_id, strategy, canonical, venue_buy, venue_sell, qty, status, reason, net_bps)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`,
		l.OpportunityID, l.PositionID, l.Strategy, l.Canonical, l.VenueBuy, l.VenueSell, l.Qty, l.Status, l.Reason, l.NetBps)
	if err != nil {
		return fmt.Errorf("insert autotrade log: %w", err)
	}
	return nil
}

// StrategyStat 对应 strategy_stats 表（D-070）。
type StrategyStat struct {
	Strategy       string
	TotalTrades    int
	Wins           int
	Losses         int
	TotalPnl       float64
	AvgPnl         float64
	WinRate        float64
	Disabled       bool
	DisabledReason string
	UpdatedAt      time.Time
}

// GetStrategyStat 读取策略统计；不存在返回零值结构（disabled=false）。
func (s *Store) GetStrategyStat(ctx context.Context, strategy string) (*StrategyStat, error) {
	var st StrategyStat
	err := s.pool.QueryRow(ctx, `
		SELECT strategy, total_trades, wins, losses, total_pnl, avg_pnl, win_rate, disabled, disabled_reason, updated_at
		FROM strategy_stats WHERE strategy = $1`, strategy).Scan(
		&st.Strategy, &st.TotalTrades, &st.Wins, &st.Losses, &st.TotalPnl, &st.AvgPnl, &st.WinRate, &st.Disabled, &st.DisabledReason, &st.UpdatedAt)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return &StrategyStat{Strategy: strategy}, nil
		}
		return nil, fmt.Errorf("get strategy stat: %w", err)
	}
	return &st, nil
}

// UpdateStrategyStat 平仓后更新策略统计（win=realizedPnl>0）。
func (s *Store) UpdateStrategyStat(ctx context.Context, strategy string, realizedPnl float64) error {
	win := 0
	if realizedPnl > 0 {
		win = 1
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO strategy_stats (strategy, total_trades, wins, losses, total_pnl, avg_pnl, win_rate, updated_at)
		VALUES ($1, 1, $2, 1-$2, $3, $3, $2, now())
		ON CONFLICT (strategy) DO UPDATE SET
			total_trades = strategy_stats.total_trades + 1,
			wins = strategy_stats.wins + $2,
			losses = strategy_stats.losses + (1-$2),
			total_pnl = strategy_stats.total_pnl + $3,
			avg_pnl = (strategy_stats.total_pnl + $3) / (strategy_stats.total_trades + 1),
			win_rate = CAST(strategy_stats.wins + $2 AS FLOAT8) / (strategy_stats.total_trades + 1),
			updated_at = now()`,
		strategy, win, realizedPnl)
	if err != nil {
		return fmt.Errorf("update strategy stat: %w", err)
	}
	return nil
}

// DisableStrategy 禁用策略（自动淘汰）。
func (s *Store) DisableStrategy(ctx context.Context, strategy, reason string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO strategy_stats (strategy, disabled, disabled_reason, updated_at)
		VALUES ($1, true, $2, now())
		ON CONFLICT (strategy) DO UPDATE SET disabled = true, disabled_reason = $2, updated_at = now()`,
		strategy, reason)
	if err != nil {
		return fmt.Errorf("disable strategy: %w", err)
	}
	return nil
}

// IsStrategyDisabled 检查策略是否被禁用。
func (s *Store) IsStrategyDisabled(ctx context.Context, strategy string) (bool, error) {
	var disabled bool
	err := s.pool.QueryRow(ctx, `SELECT disabled FROM strategy_stats WHERE strategy = $1`, strategy).Scan(&disabled)
	if err != nil {
		if err.Error() == "no rows in result set" {
			return false, nil
		}
		return false, fmt.Errorf("check strategy disabled: %w", err)
	}
	return disabled, nil
}

// DailyRealizedPnl 返回当日已平仓持仓的累计 realized_pnl（风控：单日亏损停机）。
func (s *Store) DailyRealizedPnl(ctx context.Context) (float64, error) {
	var sum *float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(realized_pnl), 0) FROM positions
		WHERE status = 'CLOSED' AND closed_at >= date_trunc('day', now())`).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("daily realized pnl: %w", err)
	}
	if sum == nil {
		return 0, nil
	}
	return *sum, nil
}
