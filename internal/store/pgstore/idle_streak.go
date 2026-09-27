// idle_streak.go —— D-534 B4：idle_floor_streak 睡钱触发层连续天数状态读写。
// PK upsert 幂等；消费面=sleepmon 日检（below_floor 连天数>=7 告警+提案行）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// IdleStreak 单池连续破地板状态（PK=venue+venue_type，与快照池键同形）。
type IdleStreak struct {
	Venue     string
	VenueType string
	Days      int       // consecutive_below_days
	CheckDay  time.Time // last_check_day（BJ 历日，同日重跑幂等锚）
	Alerted   bool      // 本轮已告警（非 below 归零才复位）
}

// GetStreaks 全量读（池数小，一次性 map 化）。
func (s *Store) GetStreaks(ctx context.Context) (map[string]IdleStreak, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT venue, venue_type, consecutive_below_days, last_check_day, alerted
		FROM idle_floor_streak`)
	if err != nil {
		return nil, fmt.Errorf("idle_floor_streak read: %w", err)
	}
	defer rows.Close()
	out := map[string]IdleStreak{}
	for rows.Next() {
		var st IdleStreak
		if err := rows.Scan(&st.Venue, &st.VenueType, &st.Days, &st.CheckDay, &st.Alerted); err != nil {
			return nil, fmt.Errorf("scan idle_floor_streak: %w", err)
		}
		out[st.Venue+"\x00"+st.VenueType] = st
	}
	return out, rows.Err()
}

// UpsertStreak PK upsert 幂等（同日重跑=同值覆写零副作用）。
func (s *Store) UpsertStreak(ctx context.Context, venue, venueType string, days int, checkDay time.Time, alerted bool) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO idle_floor_streak (venue, venue_type, consecutive_below_days, last_check_day, alerted)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (venue, venue_type) DO UPDATE SET
			consecutive_below_days=EXCLUDED.consecutive_below_days,
			last_check_day=EXCLUDED.last_check_day,
			alerted=EXCLUDED.alerted,
			updated_at=now()`,
		venue, venueType, days, checkDay, alerted)
	if err != nil {
		return fmt.Errorf("idle_floor_streak upsert %s/%s: %w", venue, venueType, err)
	}
	return nil
}
