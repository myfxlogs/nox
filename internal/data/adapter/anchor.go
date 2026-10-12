package adapter

// d162_anchor.go —— D-162R3/R4：复合报价钟构造（帧径+REST 径+DB 驱动装配共享单点）。

import (
	"context"
	"time"
)

// CompositeTickAnchor 三级链复合锚（env 径 setupMTAdapters 与 DB 径 startMT5 共用）：
// order 原生 symbol 两级（mem 内存帧携 → persist 持久末帧）全 miss →
// broker→canonical 归一（XAUUSDm→XAUUSD——ticks 落库键=发布侧 canonical）
// 再同两级；canonical 解不出/无行 → false 保留原值不猜（诚实空态）。
// persist=nil → 无持久级（仅 mem+canonical mem，测试/装配缺失照原义）。
func CompositeTickAnchor(mem, persist TickAnchor, symCanon map[string]string) TickAnchor {
	level := func(ctx context.Context, sym string) (float64, float64, time.Time, bool) {
		if b, a, ts, ok := mem(ctx, sym); ok {
			return b, a, ts, true
		}
		if persist != nil {
			if b, a, ts, ok := persist(ctx, sym); ok {
				return b, a, ts, true
			}
		}
		return 0, 0, time.Time{}, false
	}
	return func(ctx context.Context, sym string) (float64, float64, time.Time, bool) {
		if b, a, ts, ok := level(ctx, sym); ok {
			return b, a, ts, true
		}
		if canon := symCanon[sym]; canon != "" && canon != sym {
			return level(ctx, canon)
		}
		return 0, 0, time.Time{}, false
	}
}
