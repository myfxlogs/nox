// macro.go —— D-528 S3：宏观三表落库/查询（macro_event/cot_position/fred_observation）。
// upsert 全部 ON CONFLICT 幂等（重复 poll 行数不变=A1 锚）；行类型 pgstore 所有，
// internal/macro fetcher 返回本包类型（fetcher 不碰 DB，落库由本包承担）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// MacroEvent 财历事件行（source=mql5；时间一律 UTC）。
type MacroEvent struct {
	ID         int64
	Source     string
	EventTime  time.Time // UTC
	Currency   string
	Name       string
	Importance string // low/medium/high/holiday
	Forecast   string
	Previous   string
	Actual     string
	Revision   string
	Raw        []byte // jsonb
}

// CotPosition COT 周报净持产行（v1 原始字段：pct_index/momentum/conviction 暂空）。
type CotPosition struct {
	ID          int64
	ReportDate  time.Time // date（UTC 午夜）
	Instrument  string    // CME 符号（6J/GC/ES…）
	Category    string    // lev_fund/asset_mgr/dealer/managed_money/prod_merc/swap/noncomm/comm
	Direction   string    // LONG/SHORT/FLAT
	NetPosition int64
	NetChange   int64
	PctIndex    *float64
	Momentum    string
	Conviction  *float64
	Raw         []byte // jsonb
}

// FredObs FRED 观测行。
type FredObs struct {
	SeriesID string
	ObsDate  time.Time
	Value    float64
}

// UpsertMacroEvents 批量写财历事件（UNIQUE 四元组冲突更新值列）；返回写入行数。
func (s *Store) UpsertMacroEvents(ctx context.Context, events []MacroEvent) (int, error) {
	n := 0
	for _, e := range events {
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO macro_event (source, event_time_utc, currency, name, importance, forecast, previous, actual, revision, raw)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (source, event_time_utc, currency, name) DO UPDATE SET
			  importance=EXCLUDED.importance, forecast=EXCLUDED.forecast, previous=EXCLUDED.previous,
			  actual=EXCLUDED.actual, revision=EXCLUDED.revision, raw=EXCLUDED.raw`,
			e.Source, e.EventTime, e.Currency, e.Name, e.Importance,
			e.Forecast, e.Previous, e.Actual, e.Revision, e.Raw)
		if err != nil {
			return n, fmt.Errorf("upsert macro_event: %w", err)
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// UpsertCotPositions 批量写 COT 净持仓（UNIQUE 三元组冲突更新值列）；返回写入行数。
func (s *Store) UpsertCotPositions(ctx context.Context, rows []CotPosition) (int, error) {
	n := 0
	for _, r := range rows {
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO cot_position (report_date, instrument, category, direction, net_position, net_change, pct_index, momentum, conviction, raw)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			ON CONFLICT (report_date, instrument, category) DO UPDATE SET
			  direction=EXCLUDED.direction, net_position=EXCLUDED.net_position, net_change=EXCLUDED.net_change,
			  pct_index=EXCLUDED.pct_index, momentum=EXCLUDED.momentum, conviction=EXCLUDED.conviction, raw=EXCLUDED.raw`,
			r.ReportDate, r.Instrument, r.Category, r.Direction, r.NetPosition, r.NetChange,
			r.PctIndex, r.Momentum, r.Conviction, r.Raw)
		if err != nil {
			return n, fmt.Errorf("upsert cot_position: %w", err)
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// UpsertFredObs 批量写 FRED 观测（PK 冲突覆盖同日值）；返回写入行数。
func (s *Store) UpsertFredObs(ctx context.Context, rows []FredObs) (int, error) {
	n := 0
	for _, r := range rows {
		tag, err := s.pool.Exec(ctx, `
			INSERT INTO fred_observation (series_id, obs_date, value)
			VALUES ($1,$2,$3)
			ON CONFLICT (series_id, obs_date) DO UPDATE SET value=EXCLUDED.value`,
			r.SeriesID, r.ObsDate, r.Value)
		if err != nil {
			return n, fmt.Errorf("upsert fred_observation: %w", err)
		}
		n += int(tag.RowsAffected())
	}
	return n, nil
}

// MacroEventsBetween 返回 [from,to] 闭区间事件（时间升序）——渲染与 LLM 工具共用。
func (s *Store) MacroEventsBetween(ctx context.Context, from, to time.Time) ([]MacroEvent, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source, event_time_utc, currency, name, importance, forecast, previous, actual, revision, COALESCE(raw, 'null'::jsonb)
		FROM macro_event WHERE event_time_utc >= $1 AND event_time_utc <= $2
		ORDER BY event_time_utc`, from, to)
	if err != nil {
		return nil, fmt.Errorf("macro_events_between: %w", err)
	}
	defer rows.Close()
	var out []MacroEvent
	for rows.Next() {
		var e MacroEvent
		if err := rows.Scan(&e.ID, &e.Source, &e.EventTime, &e.Currency, &e.Name, &e.Importance,
			&e.Forecast, &e.Previous, &e.Actual, &e.Revision, &e.Raw); err != nil {
			return nil, fmt.Errorf("scan macro_event: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LatestCot 返回某品种最近 limit 周（report_date 降序）净持产行。
func (s *Store) LatestCot(ctx context.Context, instrument string, limit int) ([]CotPosition, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, report_date, instrument, category, direction, net_position, net_change, pct_index, momentum, conviction, COALESCE(raw, 'null'::jsonb)
		FROM cot_position WHERE instrument = $1
		ORDER BY report_date DESC, category LIMIT $2`, instrument, limit)
	if err != nil {
		return nil, fmt.Errorf("latest_cot: %w", err)
	}
	defer rows.Close()
	var out []CotPosition
	for rows.Next() {
		var r CotPosition
		if err := rows.Scan(&r.ID, &r.ReportDate, &r.Instrument, &r.Category, &r.Direction,
			&r.NetPosition, &r.NetChange, &r.PctIndex, &r.Momentum, &r.Conviction, &r.Raw); err != nil {
			return nil, fmt.Errorf("scan cot_position: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// MaxCotReportDate 全表最新 report_date（COT 调度器判定「新报告落地」基线用）。
func (s *Store) MaxCotReportDate(ctx context.Context) (time.Time, error) {
	var t *time.Time
	if err := s.pool.QueryRow(ctx, `SELECT max(report_date) FROM cot_position`).Scan(&t); err != nil {
		return time.Time{}, fmt.Errorf("max cot report_date: %w", err)
	}
	if t == nil {
		return time.Time{}, nil
	}
	return *t, nil
}
