// macro.go —— D-528 S3：宏观财历表落库/查询（macro_event）。D-120 清退 cot_position/fred_observation 面。
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

// NextHighEventAt D-053 ②：下一未来 high 事件时刻（actualsLoop 定时器臂用；无=ok false）。
func (s *Store) NextHighEventAt(ctx context.Context, now time.Time) (time.Time, bool, error) {
	var t *time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT min(event_time_utc) FROM macro_event
		WHERE importance = 'high' AND event_time_utc > $1`, now).Scan(&t)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("next high event: %w", err)
	}
	if t == nil {
		return time.Time{}, false, nil
	}
	return *t, true, nil
}

// MacroHighEventsIn D-053 ③：[from,to] 闭区间 high 事件（时间升序）；currencies 空=不过滤。
// 执行禁区闸（开仓/平仓推迟）与 actuals 推送扫描共用单查。
func (s *Store) MacroHighEventsIn(ctx context.Context, from, to time.Time, currencies []string) ([]MacroEvent, error) {
	q := `SELECT id, source, event_time_utc, currency, name, importance, forecast, previous, actual, revision, COALESCE(raw, 'null'::jsonb)
		FROM macro_event WHERE importance = 'high' AND event_time_utc >= $1 AND event_time_utc <= $2`
	args := []any{from, to}
	if len(currencies) > 0 {
		q += ` AND currency = ANY($3)`
		args = append(args, currencies)
	}
	q += ` ORDER BY event_time_utc`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("macro high events in: %w", err)
	}
	defer rows.Close()
	var out []MacroEvent
	for rows.Next() {
		var e MacroEvent
		if err := rows.Scan(&e.ID, &e.Source, &e.EventTime, &e.Currency, &e.Name, &e.Importance,
			&e.Forecast, &e.Previous, &e.Actual, &e.Revision, &e.Raw); err != nil {
			return nil, fmt.Errorf("scan macro_event high: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// CountMacroBlackoutRejects D-053 ④：24h 窗 macro 禁区拒开行数（autotrade_log REJECTED
// ∧ reason 前缀 macro blackout——gate_macro_blackout rejectLog 固定文案）。
func (s *Store) CountMacroBlackoutRejects(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM autotrade_logs
		WHERE status = 'REJECTED' AND reason LIKE 'macro blackout%' AND ts >= $1`, since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count macro blackout rejects: %w", err)
	}
	return n, nil
}
