package dashboard

// leverage_cached_test.go —— D-574 锚：List 径 venueLeverageCached 限 ctx
// 读穿——venue 挂死时 ≤4s 降级 honest 缺不挂页；命中缓存秒回零实拉。

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestVenueLeverageCached_TimeoutDegrades(t *testing.T) {
	s := &Service{}
	s.venueLeverageFn = func(ctx context.Context, _, _ string, _ bool) (float64, error) {
		select {
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-time.After(30 * time.Second):
			return 5, nil
		}
	}
	st := time.Now()
	lev, err := s.venueLeverageCached(context.Background(), "gate:perp", "X_USDT", true)
	if el := time.Since(st); el > 6*time.Second {
		t.Fatalf("挂死 venue 应 4s 内降级，耗 %v", el)
	}
	if err == nil || lev > 0 {
		t.Fatalf("限时应返 err+0, got lev=%v err=%v", lev, err)
	}
}

func TestVenueLeverageCached_CacheHit(t *testing.T) {
	s := &Service{}
	calls := 0
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) {
		calls++
		return 25, nil
	}
	for i := 0; i < 3; i++ {
		lev, err := s.venueLeverageCached(context.Background(), "okx:perp", "X-USDT-SWAP", false)
		if err != nil || lev != 25 {
			t.Fatalf("hit 应返 25: lev=%v err=%v", lev, err)
		}
	}
	if calls != 1 {
		t.Fatalf("5min 读穿窗内应只实拉 1 次, got %d", calls)
	}
	// 错值不入缓存——下次仍实拉。
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) {
		return 0, errors.New("venue down")
	}
	if _, err := s.venueLeverageCached(context.Background(), "binance:perp", "YUSDT", true); err == nil {
		t.Fatal("读败应透 err")
	}
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) {
		calls++
		return 10, nil
	}
	if lev, err := s.venueLeverageCached(context.Background(), "binance:perp", "YUSDT", true); err != nil || lev != 10 {
		t.Fatalf("败值不缓存，重拉应成: lev=%v err=%v", lev, err)
	}
}
