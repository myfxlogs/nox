// facts_tick_mid.go —— LatestTickMid/LatestTickMidWithin（棘轮拆出件零行为变）。
package pgstore

import (
	"context"
	"time"
)

// LatestTickMid 指定 venue+symbol 最近 bid/ask 中间价（D-359 TRX USD 价三级 fallback 末级）。
// 补 venue 过滤——同 canonical 多 venue 并行采样时取错 venue 价即脏锚。
// 无行/非正价 → false（诚实缺，不猜）。
func (s *Store) LatestTickMid(ctx context.Context, venue, symbol string) (float64, bool) {
	var v float64
	err := s.pool.QueryRow(ctx, `
		SELECT (bid + ask) / 2 FROM ticks
		WHERE venue = $1 AND symbol = $2 AND bid > 0 AND ask > 0
		ORDER BY ts DESC LIMIT 1`, venue, symbol).Scan(&v)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// LatestTickMidWithin D-604 N2：LatestTickMid + ts 下界窗（判定径专用——
// 陈价进真金 sizing 病灶；maxAge<=0=无窗保旧语义，调用方负 fail-closed 责）。
func (s *Store) LatestTickMidWithin(ctx context.Context, venue, symbol string, maxAge time.Duration) (float64, bool) {
	var v float64
	var ts time.Time
	err := s.pool.QueryRow(ctx, `
		SELECT (bid + ask) / 2, ts FROM ticks
		WHERE venue = $1 AND symbol = $2 AND bid > 0 AND ask > 0
		ORDER BY ts DESC LIMIT 1`, venue, symbol).Scan(&v, &ts)
	if err != nil || v <= 0 {
		return 0, false
	}
	if maxAge > 0 && time.Since(ts) > maxAge {
		return 0, false // 陈值 fail-closed
	}
	return v, true
}
