// ticks.go —— D-495 S1：腿可平性谓词的数据面（ticks 表窗口内 bid 值变化计数）。
// 判据出处：KB heartbeat-quote-fools-freshness-gate——可交易=窗口内 bid 值发生变化≥2 种；
// 单值（冻结报价/冷启动首见）与时间戳新鲜均不等于可交易。
package pgstore

import (
	"context"
	"fmt"
	"nox/internal/data/keydomain"
	"time"
)

// TicksPriceChanged 窗口内 (venue, symbol) 的 DISTINCT bid 数。
// changed = n >= 2（价格动过=市场活在撮合）；n 供调用方拼诊断文案。
// venue/symbol 是 ticks 表原生键（裸 venue 名 + venue 原生 symbol 或 CEX 归一键），
// 键域映射由调用方负责（leg_actionable.go），本函数不做键形转换。
func (s *Store) TicksPriceChanged(ctx context.Context, venue, symbol string, window time.Duration) (changed bool, n int, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT bid)::int FROM ticks
		WHERE venue = $1 AND symbol = $2 AND ts > now() - $3::interval`,
		venue, symbol, window.String()).Scan(&n)
	if err != nil {
		return false, 0, fmt.Errorf("ticks price changed %s/%s: %w", venue, symbol, err)
	}
	return n >= 2, n, nil
}

// VenueTickLatestAt D-170：venue 级活性证——ticks 表该 venue 最新帧时刻。
// ok=false=该 venue 零行（无证据，不等同死——由调用方语义裁定）。
func (s *Store) VenueTickLatestAt(ctx context.Context, venue string) (time.Time, bool, error) {
	var t *time.Time
	// venue_key=生成列零索引——键形拆 (venue,venue_type) 双参/裸 venue 单参（同族形）。
	k := keydomain.Parse(venue)
	q := `SELECT max(ts) FROM ticks WHERE venue = $1`
	args := []any{k.Venue}
	if k.Type != "" {
		q += ` AND venue_type = $2`
		args = append(args, k.Type)
	}
	err := s.pool.QueryRow(ctx, q, args...).Scan(&t)
	if err != nil {
		return time.Time{}, false, fmt.Errorf("venue tick latest %s: %w", venue, err)
	}
	if t == nil {
		return time.Time{}, false, nil
	}
	return *t, true, nil
}
