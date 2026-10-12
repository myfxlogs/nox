// retro.go —— D-457 sysretro 复盘报告数据面：日报/周报窗口统计查询。
// 全确定性 SQL——报告数字唯一真相源，AI 侧只叙事不改数（D-442 同纪律）。
package pgstore

import (
	"context"
	"math"
	"sort"
	"time"
)

// RetroOpenPos 日报§1 OPEN 仓行（venue=首腿 OPEN 腿 venue，carry=累计实收现金流）。
type RetroOpenPos struct {
	ID       int64
	Type     string
	Venue    string
	OpenedAt time.Time
	CarryUSD float64
}

// RetroDaily 日报窗口统计（dayStart~dayEnd，UTC 日界由调用方给）。
type RetroDaily struct {
	OpenPos          []RetroOpenPos
	OpenedToday      int
	ClosedToday      int
	GraduationsToday int     // D-468 S4：窗口内 carry_graduations.created_at 计数（毕业建议新增）
	FundingActualUSD float64 // 当日 kind=funding quality=actual 实收
	FundingEstUSD    float64 // 当日 kind=funding quality=estimated 预估口径
	EvalDist         map[string]int
	AlertDist        map[string]int
	AlertTop3        []string
	StreamEvents     int // message 含 断/重连/zero-frame 类计数
}

// RetroClosedGroup 周报§1 平仓分组（按 type）。
type RetroClosedGroup struct {
	Type    string
	N       int
	Wins    int
	Losses  int
	PnlUSD  float64
	EstBps  float64 // expected_net_bps 合计（开仓预估口径）
	RealBps float64 // realized_pnl/notional 合计 bps 化
}

// RetroClosedCase 周报§1 典型案例行。
type RetroClosedCase struct {
	ID      int64
	Type    string
	PnlUSD  float64
	EstBps  float64
	RealBps float64
}

// RetroStrategyRow 周报§3 strategy_stats 行。
// HPR 三列（D-181）：per-trade 资本效率口径——avg(net_realized_usd/notional_usd)，
// 名义本金为资本占用代理（positions 无保证金列）；HprAvg=nil 或 HprN=0 → 口径缺。
type RetroStrategyRow struct {
	Strategy string
	Trades   int
	WinRate  float64
	TotalPnl float64
	Disabled bool
	Reason   string
	HprAvg   *float64 // 平均每笔 HPR 比率（纯证据字段，不入淘汰判定）
	HprN     int      // notional 可得行数
	HprAll   int      // 聚合总行数
}

// RetroAlertCluster 周报§4 告警聚类行（按 message 首词聚类）。
type RetroAlertCluster struct {
	Key   string
	Count int
	Level string
}

// RetroWeekly 周报窗口统计。
type RetroWeekly struct {
	Groups       []RetroClosedGroup
	TopWin       *RetroClosedCase
	TopLoss      *RetroClosedCase
	TopDeviation *RetroClosedCase // |est-real| 最大
	PaperSpot    map[string]int   // exit_reason 分布（窗口内 exit_ts 落窗）
	PaperIntra   map[string]int   // exit_reason 分布
	PaperIntraN  int              // 窗口内平仓计数
	PaperSpotN   int
	Strategies   []RetroStrategyRow
	AlertCluster []RetroAlertCluster
	EvalDist     map[string]int
	// DepSeq D-180：每策略按 settled_at 序的净 PNL 符号序列（转折点检验输入，
	// 与 AggregateStrategyStats 同排除口径；纯证据字段）。
	DepSeq map[string][]float64
}

// RetroDailyStats 日报数据面。
func (s *Store) RetroDailyStats(ctx context.Context, start, end time.Time) (*RetroDaily, error) {
	d := &RetroDaily{EvalDist: map[string]int{}, AlertDist: map[string]int{}}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.type, COALESCE(l.venue_key,''), p.opened_at,
		       COALESCE((SELECT sum(amount_usd) FROM position_cashflows c WHERE c.position_id=p.id AND quality='actual'),0)
		FROM positions p
		LEFT JOIN position_legs l ON l.position_id=p.id AND l.phase='OPEN' AND l.leg_no=1
		WHERE p.status IN ('OPEN','OPENING','LEGGED','CLOSING')
		ORDER BY p.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var r RetroOpenPos
		var oa *time.Time
		if err := rows.Scan(&r.ID, &r.Type, &r.Venue, &oa, &r.CarryUSD); err != nil {
			return nil, err
		}
		if oa != nil {
			r.OpenedAt = *oa
		}
		d.OpenPos = append(d.OpenPos, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FILTER (WHERE opened_at >= $1 AND opened_at < $2),
		       count(*) FILTER (WHERE closed_at >= $1 AND closed_at < $2)
		FROM positions`, start, end).Scan(&d.OpenedToday, &d.ClosedToday); err != nil {
		return nil, err
	}
	// 毕业建议窗口计数（持续零产出→次日日报可见）。
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM carry_graduations WHERE created_at >= $1 AND created_at < $2`,
		start, end).Scan(&d.GraduationsToday); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(sum(amount_usd) FILTER (WHERE quality='actual'),0),
		       COALESCE(sum(amount_usd) FILTER (WHERE quality='estimated'),0)
		FROM position_cashflows WHERE kind='funding' AND ts >= $1 AND ts < $2`,
		start, end).Scan(&d.FundingActualUSD, &d.FundingEstUSD); err != nil {
		return nil, err
	}
	if err := s.scanDist(ctx, &d.EvalDist, `
		SELECT decision, count(*) FROM position_evals
		WHERE evaluated_at >= $1 AND evaluated_at < $2 GROUP BY decision`, start, end); err != nil {
		return nil, err
	}
	if err := s.scanDist(ctx, &d.AlertDist, `
		SELECT level, count(*) FROM alerts
		WHERE ts >= $1 AND ts < $2 GROUP BY level`, start, end); err != nil {
		return nil, err
	}
	arows, err := s.pool.Query(ctx, `
		SELECT message FROM alerts WHERE ts >= $1 AND ts < $2
		ORDER BY CASE level WHEN 'critical' THEN 0 WHEN 'warn' THEN 1 ELSE 2 END, ts DESC LIMIT 3`,
		start, end)
	if err != nil {
		return nil, err
	}
	defer arows.Close()
	for arows.Next() {
		var m string
		if err := arows.Scan(&m); err != nil {
			return nil, err
		}
		d.AlertTop3 = append(d.AlertTop3, m)
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM alerts WHERE ts >= $1 AND ts < $2
		AND (message ~* 'disconnect|reconnect|zero.?frame|断流|重连')`, start, end).Scan(&d.StreamEvents); err != nil {
		return nil, err
	}
	return d, nil
}

// RetroWeeklyStats 周报数据面（窗口 [since,until)）。
func (s *Store) RetroWeeklyStats(ctx context.Context, since, until time.Time) (*RetroWeekly, error) {
	w := &RetroWeekly{
		PaperSpot: map[string]int{}, PaperIntra: map[string]int{}, EvalDist: map[string]int{},
	}
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.type, COALESCE(p.realized_pnl,0),
		       COALESCE(p.expected_net_bps,0),
		       CASE WHEN COALESCE(p.notional_usd,0)>0 THEN p.realized_pnl/p.notional_usd*10000 ELSE 0 END
		FROM positions p
		WHERE p.status='CLOSED' AND p.closed_at >= $1 AND p.closed_at < $2`, since, until)
	if err != nil {
		return nil, err
	}
	typeAgg := map[string]*RetroClosedGroup{}
	maxDev := -1.0
	for rows.Next() {
		var c RetroClosedCase
		if err := rows.Scan(&c.ID, &c.Type, &c.PnlUSD, &c.EstBps, &c.RealBps); err != nil {
			rows.Close()
			return nil, err
		}
		g := typeAgg[c.Type]
		if g == nil {
			g = &RetroClosedGroup{Type: c.Type}
			typeAgg[c.Type] = g
		}
		g.N++
		g.PnlUSD += c.PnlUSD
		g.EstBps += c.EstBps
		g.RealBps += c.RealBps
		if c.PnlUSD > 0 {
			g.Wins++
		} else {
			g.Losses++
		}
		if w.TopWin == nil || c.PnlUSD > w.TopWin.PnlUSD {
			cc := c
			w.TopWin = &cc
		}
		if w.TopLoss == nil || c.PnlUSD < w.TopLoss.PnlUSD {
			cc := c
			w.TopLoss = &cc
		}
		if dev := math.Abs(c.RealBps - c.EstBps); dev > maxDev {
			maxDev = dev
			cc := c
			w.TopDeviation = &cc
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for _, g := range typeAgg {
		w.Groups = append(w.Groups, *g)
	}
	sort.Slice(w.Groups, func(i, j int) bool { return w.Groups[i].Type < w.Groups[j].Type })
	if err := s.scanDist(ctx, &w.PaperSpot, `
		SELECT exit_reason, count(*) FROM paper_spot_carry
		WHERE exit_ts >= $1 AND exit_ts < $2 GROUP BY exit_reason`, since, until); err != nil {
		return nil, err
	}
	if err := s.scanDist(ctx, &w.PaperIntra, `
		SELECT exit_reason, count(*) FROM paper_intra_trades
		WHERE status='closed' AND exit_ts >= $1 AND exit_ts < $2 GROUP BY exit_reason`, since, until); err != nil {
		return nil, err
	}
	if err := s.pool.QueryRow(ctx, `
		SELECT (SELECT count(*) FROM paper_intra_trades WHERE status='closed' AND exit_ts >= $1 AND exit_ts < $2),
		       (SELECT count(*) FROM paper_spot_carry WHERE exit_ts >= $1 AND exit_ts < $2)`,
		since, until).Scan(&w.PaperIntraN, &w.PaperSpotN); err != nil {
		return nil, err
	}
	srows, err := s.retroStrategyRows(ctx)
	if err != nil {
		return nil, err
	}
	w.Strategies = srows
	if err := s.scanDist(ctx, &w.EvalDist, `
		SELECT decision, count(*) FROM position_evals
		WHERE evaluated_at >= $1 AND evaluated_at < $2 GROUP BY decision`, since, until); err != nil {
		return nil, err
	}
	crows, err := s.pool.Query(ctx, `
		SELECT split_part(message,' ',1), level, count(*) AS n FROM alerts
		WHERE ts >= $1 AND ts < $2 AND level IN ('warn','critical')
		GROUP BY 1, level ORDER BY n DESC LIMIT 10`, since, until)
	if err != nil {
		return nil, err
	}
	defer crows.Close()
	for crows.Next() {
		var c RetroAlertCluster
		if err := crows.Scan(&c.Key, &c.Level, &c.Count); err != nil {
			return nil, err
		}
		w.AlertCluster = append(w.AlertCluster, c)
	}
	depSeq, err := s.retroStrategySequences(ctx)
	if err != nil {
		return nil, err
	}
	w.DepSeq = depSeq
	return w, nil
}
