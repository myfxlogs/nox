package pgstore

import (
	"context"
	"time"
)

// RepoRate 是 fin_repo_rates 表的一行（D-266 CNY 逆回购利率雷达）。
type RepoRate struct {
	ID        int64
	Code      string   // "204001" / "204007" / "204014" / "204028"
	Name      string   // "GC001" / "GC007" / ...
	RatePct   float64  // 年化利率%
	ChangePct *float64 // 涨跌幅%（可空=nil，合法零=&0.0）
	Ts        time.Time
	TradeDate time.Time // 交易日（北京时间，DATE 列）
}

// InsertRepoRate 插入一条逆回购利率快照（D-266 §2.2）。
// 不做 upsert——保留日内逐笔快照（历史回看+基线建立）。
// R1 修复：tradeDate 传 Format("2006-01-02") 字符串，避免 pgx DATE 列按 UTC 编码导致
// 上海午夜偏移到前一天（time.Date(...,CST) → pgx 按 UTC → DATE 存前一天）。
func (s *Store) InsertRepoRate(ctx context.Context, code, name string, ratePct float64, changePct *float64, tradeDate time.Time) error {
	_, err := s.pool.Exec(ctx,
		`INSERT INTO fin_repo_rates (code, name, rate_pct, change_pct, trade_date)
		 VALUES ($1, $2, $3, $4, $5)`,
		code, name, ratePct, changePct, tradeDate.Format("2006-01-02"))
	return err
}

// LatestRepoRates 返回每个 code 的最新一行（DISTINCT ON + ORDER BY ts DESC）。
// 设计 §2.3.1：雷达 RPC 取 4 品种最新利率。
func (s *Store) LatestRepoRates(ctx context.Context) ([]RepoRate, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT DISTINCT ON (code) id, code, name, rate_pct, change_pct, ts, trade_date
		 FROM fin_repo_rates ORDER BY code, ts DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []RepoRate
	for rows.Next() {
		var r RepoRate
		if err := rows.Scan(&r.ID, &r.Code, &r.Name, &r.RatePct, &r.ChangePct, &r.Ts, &r.TradeDate); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// RepoRate30dAvg 返回指定 code 最近 30 天的 rate_pct 均值（GC007 基线）。
// 冷启动（无数据）返回 (0, false, nil)，不报错。
// COALESCE 兜底：AVG 无数据时返回 NULL → 0（与 OHLC 同款约束）。
func (s *Store) RepoRate30dAvg(ctx context.Context, code string) (float64, bool, error) {
	var avg float64
	var has bool
	err := s.pool.QueryRow(ctx,
		`SELECT COALESCE(AVG(rate_pct), 0), COUNT(*) > 0 FROM fin_repo_rates
		 WHERE code = $1 AND ts >= now() - interval '30 days'`, code).Scan(&avg, &has)
	if err != nil {
		return 0, false, err
	}
	return avg, has, nil
}

// RepoRateDailyOHLC 返回指定 code 指定交易日的开/收/高/低（聚合查询）。
// 设计 §2.2：日级聚合由查询时聚合，不预计算。
// RepoRateDailyOHLC 钉死约束：open/close 子查询 COALESCE((...), 0) 与 high/low 同款——
// 当日无数据时返回全零，禁返回 NULL（空表场景 handler 靠它不报错）。
func (s *Store) RepoRateDailyOHLC(ctx context.Context, code string, date time.Time) (open, close_, high, low float64, err error) {
	// 当日按 ts 升序：open=first, close=last, high=max, low=min。
	// COALESCE 兜底：无数据时全零不报错（设计 §S1 断言）。
	err = s.pool.QueryRow(ctx, `
		SELECT
			COALESCE((SELECT rate_pct FROM fin_repo_rates
			          WHERE code = $1 AND trade_date = $2
			          ORDER BY ts ASC LIMIT 1), 0),
			COALESCE((SELECT rate_pct FROM fin_repo_rates
			          WHERE code = $1 AND trade_date = $2
			          ORDER BY ts DESC LIMIT 1), 0),
			COALESCE((SELECT MAX(rate_pct) FROM fin_repo_rates
			          WHERE code = $1 AND trade_date = $2), 0),
			COALESCE((SELECT MIN(rate_pct) FROM fin_repo_rates
			          WHERE code = $1 AND trade_date = $2), 0)`,
		code, date.Format("2006-01-02")).Scan(&open, &close_, &high, &low)
	return
}

// AlreadyAlertedToday 查询今日是否已发过匹配 pattern 的告警（去重，设计 §2.3.2）。
// pattern 用 LIKE 匹配，如 "[repo] GC007%"（含品种代码，每品种独立去重——审计 A1 修正）。
func (s *Store) AlreadyAlertedToday(ctx context.Context, pattern string) (bool, error) {
	var exists bool
	err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM alerts
		 WHERE message LIKE $1 AND ts::date = CURRENT_DATE)`, pattern).Scan(&exists)
	if err != nil {
		return false, err
	}
	return exists, nil
}

// RepoRate30dPercentile 返回指定 code 当前利率在 30 日分布中的分位数（0-100）。
// 冷启动（无数据）返回 (0, false, nil)——不阻塞提醒，分位数仅用于展示。
// 用 COUNT FILTER 计算（精确分位数，无需 PG 特定函数）。
func (s *Store) RepoRate30dPercentile(ctx context.Context, code string, currentRate float64) (float64, bool, error) {
	var total, below int64
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*), COUNT(*) FILTER (WHERE rate_pct <= $2)
		 FROM fin_repo_rates WHERE code = $1 AND ts >= now() - interval '30 days'`,
		code, currentRate).Scan(&total, &below)
	if err != nil {
		return 0, false, err
	}
	if total == 0 {
		return 0, false, nil
	}
	return float64(below) / float64(total) * 100, true, nil
}
