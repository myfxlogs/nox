package pgstore

import (
	"context"
	"fmt"

	"arb/internal/keydomain"
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
	vb, vs := keydomain.Parse(l.VenueBuy), keydomain.Parse(l.VenueSell) // D-496 复合键拆两列
	_, err := s.pool.Exec(ctx, `
		INSERT INTO autotrade_logs (opportunity_id, position_id, strategy, canonical, venue_buy, venue_type_buy, venue_sell, venue_type_sell, qty, status, reason, net_bps)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		l.OpportunityID, l.PositionID, l.Strategy, l.Canonical, vb.Venue, vb.Type, vs.Venue, vs.Type, l.Qty, l.Status, l.Reason, l.NetBps)
	if err != nil {
		return fmt.Errorf("insert autotrade log: %w", err)
	}
	return nil
}

// CountComboStopLosses D-256-A S4：组合级止损计数（reason-aware）。
// 查 autotrade_logs 中 status='CLOSE_TRIGGER' 且 reason 匹配止损前缀（basis loss / net swap）
// 且 ts > now() - windowDays 天的行，按 opportunity_id 分组计数。
// 返回 map[oppID]count，调用方用 combo.FromOppID 归并到 ComboKey。
// reason 前缀过滤：排除 manual_rpc / probe 等非止损触发（D-185 批评无差别 cooldown 误杀成立）。
func (s *Store) CountComboStopLosses(ctx context.Context, windowDays int) (map[string]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT opportunity_id, count(*) AS cnt
		FROM autotrade_logs
		WHERE status = 'CLOSE_TRIGGER'
		  AND (reason LIKE 'exit: basis loss%' OR reason LIKE 'exit: net swap%')
		  AND ts > now() - make_interval(days => $1)
		GROUP BY opportunity_id`, windowDays)
	if err != nil {
		return nil, fmt.Errorf("count combo stop losses: %w", err)
	}
	defer rows.Close()
	out := make(map[string]int64)
	for rows.Next() {
		var oppID string
		var cnt int64
		if err := rows.Scan(&oppID, &cnt); err != nil {
			return nil, fmt.Errorf("scan combo stop loss row: %w", err)
		}
		out[oppID] = cnt
	}
	return out, rows.Err()
}

// StrategyStat 对应 strategy_stats 表（D-070）。
// D-154 起淘汰判定 v2 不读本表累计列（total/wins/avg/win_rate），仅 disabled/disabled_reason（启停载体）。
// 累计列仍由 UpdateStrategyStat 写，仅供显示层滚动统计——非淘汰真相源（P3）。
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

// AggregateStat 淘汰判定 v2 实时聚合结果（D-154 S1）。
// 替代 strategy_stats 累计列——positions + position_cashflows 实时聚合，覆盖 LEGGED/skip 等不入 stat 的平仓。
// net 口径 = D-127 P4 SumCashflowsNet 同口径（realized + swap + funding − commission）。
// realized_pnl IS NOT NULL = 可判定口径（LEGGED 历史清理仓 realized NULL 不计入，避免按 0 稀释）。
// 0 笔时 Total=0、Median=0（NULL→Scan 0，调用方按 0 处理 → 小样本判据 median<0 永不成立 → 不杀）。
type AggregateStat struct {
	Strategy  string
	Total     int
	Wins      int
	AvgPnl    float64
	MedianPnl float64
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

// AggregateStrategyStats 实时聚合策略全量平仓净值（D-154 S1 淘汰判定 v2 真相源）。
// 替代 strategy_stats 累计列——position_settlements 实时聚合，覆盖 LEGGED/skip 等不入 stat 的平仓。
// D-193-A：net 改读 settlement.net_realized_usd（正式净 PNL），legacy 行 fallback realized_usd。
// realized_pnl IS NOT NULL = 可判定口径（LEGGED 历史清理仓 realized NULL 不计入）。
// D-323 S1：stats_status 过滤（COALESCE NULL→applied=legacy 兼容）——skipped/retryable/pending
// 行出局，统计层只消费数据质量过关的结算（D-193-A 原意闭环）。
// 0 笔时 total=0、wins=0、avg=0、median=0（COALESCE 兜底 NULL→0，调用方按 0 处理 → 小样本判据 median<0 永不成立 → 不杀）。
func (s *Store) AggregateStrategyStats(ctx context.Context, strategy string) (AggregateStat, error) {
	var st AggregateStat
	st.Strategy = strategy
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(total, 0), COALESCE(wins, 0), COALESCE(avg_pnl, 0), COALESCE(median_pnl, 0)
		FROM (
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE net > 0) AS wins,
			       avg(net) AS avg_pnl,
			       percentile_cont(0.5) WITHIN GROUP (ORDER BY net) AS median_pnl
			FROM (
				SELECT COALESCE(s.net_realized_usd, s.realized_usd) AS net
				FROM position_settlements s
				JOIN positions p ON p.id = s.position_id
				WHERE p.type = $1 AND p.closed_at IS NOT NULL AND p.realized_pnl IS NOT NULL
				  AND COALESCE(s.stats_status, 'applied') = 'applied' AND NOT %s AND %s
			) t
		) agg`, incidentPositionPredicate, probeExclusionPredicate), strategy).Scan(&st.Total, &st.Wins, &st.AvgPnl, &st.MedianPnl)
	if err != nil {
		return AggregateStat{}, fmt.Errorf("aggregate strategy stats: %w", err)
	}
	return st, nil
}

// UpdateStrategyStat 平仓后更新策略统计（win=realizedPnl>0）。
// D-154 起：累计列仅供显示层滚动统计，淘汰判定 v2 不读本表（改 AggregateStrategyStats 实时聚合）。
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

// ListDisabledStrategies 返回所有 disabled=true 的策略名集合。
// 供 engine 检测层排除 disabled 策略产出的机会（§1 自动淘汰闭环：disable → 不执行 + 不产机会 + 不展示）。
func (s *Store) ListDisabledStrategies(ctx context.Context) (map[string]bool, error) {
	rows, err := s.pool.Query(ctx, `SELECT strategy FROM strategy_stats WHERE disabled = true`)
	if err != nil {
		return nil, fmt.Errorf("list disabled strategies: %w", err)
	}
	defer rows.Close()
	out := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan disabled strategy: %w", err)
		}
		out[name] = true
	}
	return out, rows.Err()
}

// DailyRealizedPnl 返回当日已平仓持仓的累计净 PNL（风控：单日亏损停机）。
// D-193-A 扩包（决策 9）：与 AggregateStrategyStats（autotrade.go:98）同构——
// LEFT JOIN position_settlements + COALESCE 三级 fallback（net_realized_usd → realized_usd → realized_pnl）。
// 禁止 actual-only 过滤——这是单日亏损停机风控，缺数据静默归零=哑数据击穿风控（D-223 R1 同型）。
// 无 settlement 行/legacy 行经 fallback 覆盖。
func (s *Store) DailyRealizedPnl(ctx context.Context) (float64, error) {
	var sum *float64
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(COALESCE(s.net_realized_usd, s.realized_usd, p.realized_pnl)), 0)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE p.status = 'CLOSED' AND p.closed_at >= date_trunc('day', now()) AND NOT %s AND %s`,
		incidentPositionPredicate, probeExclusionPredicate)).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("daily realized pnl: %w", err)
	}
	if sum == nil {
		return 0, nil
	}
	return *sum, nil
}

// GetPnlAnchor 返回盈亏统计起点（F4，stats_anchor 单行表）。
// D-193-A 扩包（决策 9）：累计已实现口径=SUM(net_realized_usd)（RealizedPnlSinceAnchor 同构）。
// 本函数只读 stats_anchor 表无 SQL 可改，仅注释口径顺带对齐。
func (s *Store) GetPnlAnchor(ctx context.Context) (time.Time, error) {
	var anchor time.Time
	err := s.pool.QueryRow(ctx, `SELECT anchor FROM stats_anchor WHERE id = 1`).Scan(&anchor)
	if err != nil {
		return time.Time{}, fmt.Errorf("get pnl anchor: %w", err)
	}
	return anchor, nil
}

// RealizedPnlSinceAnchor 返回 closed_at >= anchor 的累计已实现盈亏（F4 后端口径）。
// D-193-A 扩包（决策 9）：与 AggregateStrategyStats/DailyRealizedPnl 同构——
// LEFT JOIN position_settlements + COALESCE 三级 fallback。禁止 actual-only 过滤（风控哑数据击穿）。
func (s *Store) RealizedPnlSinceAnchor(ctx context.Context) (float64, error) {
	anchor, err := s.GetPnlAnchor(ctx)
	if err != nil {
		return 0, err
	}
	var sum *float64
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(s.net_realized_usd, s.realized_usd, p.realized_pnl)), 0)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE p.status = 'CLOSED' AND p.closed_at >= $1`, anchor).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("realized pnl since anchor: %w", err)
	}
	if sum == nil {
		return 0, nil
	}
	return *sum, nil
}

// RealizedPnlToday 当日已实现（closed_at>=DB 当日零点，与 RealizedPnlSinceAnchor 同三级
// COALESCE 口径；D-508 S8 前端聚合下沉——「今日」口径以 DB 时钟为准不再依赖浏览器时区）。
func (s *Store) RealizedPnlToday(ctx context.Context) (float64, error) {
	var sum *float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(COALESCE(s.net_realized_usd, s.realized_usd, p.realized_pnl)), 0)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE p.status = 'CLOSED' AND p.closed_at >= date_trunc('day', now())`).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("realized pnl today: %w", err)
	}
	if sum == nil {
		return 0, nil
	}
	return *sum, nil
}

// ResetPnlAnchor 重置盈亏统计起点为 now()（F4，业主点「清零」→ anchor=now()）。
// 返回重置后的 anchor。历史 CLOSED 持仓不删除（业主选 A「不删历史」）。
func (s *Store) ResetPnlAnchor(ctx context.Context) (time.Time, error) {
	var anchor time.Time
	err := s.pool.QueryRow(ctx, `
		UPDATE stats_anchor SET anchor = now() WHERE id = 1 RETURNING anchor`).Scan(&anchor)
	if err != nil {
		return time.Time{}, fmt.Errorf("reset pnl anchor: %w", err)
	}
	return anchor, nil
}
