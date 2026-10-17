package dashboard

// D-337 T1/T2：nextSwapChargeAtInLoc（server 零点换算，LoadLocation 防 DST）+
// venue 时区缓存（TTL 内单 fetch；失败 nil 不缓存正结果，下轮重试）。
import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestD337_NextSwapChargeAtInLoc(t *testing.T) {
	athens, err := time.LoadLocation("Europe/Athens")
	if err != nil {
		t.Fatalf("LoadLocation Athens: %v（禁硬编码时区，名不可解析→nil 语义）", err)
	}
	eq := func(a, b time.Time) bool { return a.Equal(b) }
	// 常规（EEST +3）：10:00 → 当日剩余=至次日 00:00 Athens（=前日 21:00 UTC）
	now := time.Date(2026, 6, 10, 10, 0, 0, 0, athens)
	if got := nextSwapChargeAtInLoc(now, athens); !eq(got, time.Date(2026, 6, 11, 0, 0, 0, 0, athens)) {
		t.Errorf("regular: got %v want 2026-06-11 00:00 Athens", got)
	}
	// 跨日（01:30 雅典凌晨 → 次日 00:00）——雅典日≠UTC 日的时段（UTC=前日 22:30），
	// 去时区化变异（按 UTC 算）在此必红：白昼时刻锚对 UTC 化不敏感（lesson: 锚要打在差异轴上）。
	now = time.Date(2026, 6, 11, 1, 30, 0, 0, athens)
	if got := nextSwapChargeAtInLoc(now, athens); !eq(got, time.Date(2026, 6, 12, 0, 0, 0, 0, athens)) {
		t.Errorf("cross-day: got %v want 2026-06-12 00:00 Athens", got)
	}
	// DST 边界前（10-24 EEST +3）：次日 10-25 00:00 Athens = 10-24 21:00 UTC
	now = time.Date(2026, 10, 24, 12, 0, 0, 0, athens)
	if got := nextSwapChargeAtInLoc(now, athens); !eq(got, time.Date(2026, 10, 25, 0, 0, 0, 0, athens)) {
		t.Errorf("DST-before: got %v want 2026-10-25 00:00 Athens", got)
	}
	// DST 边界后（10-26 EET +2）：次日 10-27 00:00 Athens = 10-26 22:00 UTC
	now = time.Date(2026, 10, 26, 12, 0, 0, 0, athens)
	if got := nextSwapChargeAtInLoc(now, athens); !eq(got, time.Date(2026, 10, 27, 0, 0, 0, 0, athens)) {
		t.Errorf("DST-after: got %v want 2026-10-27 00:00 Athens", got)
	}
	// DST 切换日凌晨（10-25 01:00 EEST +3，未到 04:00 切换）：次日 10-26 00:00 Athens=10-25 22:00 UTC。
	// 凌晨锚（雅典日≠UTC 日）——去时区化变异必红。
	now = time.Date(2026, 10, 25, 1, 0, 0, 0, athens)
	if got := nextSwapChargeAtInLoc(now, athens); !eq(got, time.Date(2026, 10, 26, 0, 0, 0, 0, athens)) {
		t.Errorf("DST-transition-day: got %v want 2026-10-26 00:00 Athens", got)
	}
	// UTC 换算锚（DST 后 +2：10-25 22:00 UTC）
	if got := nextSwapChargeAtInLoc(time.Date(2026, 10, 26, 12, 0, 0, 0, athens), athens).UTC(); !got.Equal(time.Date(2026, 10, 26, 22, 0, 0, 0, time.UTC)) {
		t.Errorf("UTC conversion want 2026-10-26 22:00 UTC, got %v", got)
	}
}

func TestD337_ServerTZCache(t *testing.T) {
	ctx := context.Background()
	fetches := 0
	c := NewServerTZCache(time.Hour, func(_ context.Context, _ string) (float64, error) {
		fetches++
		return 3, nil
	})
	loc1 := c.Get(ctx, "v:mt5")
	if loc1 == nil {
		t.Fatal("first get want loc, got nil")
	}
	loc2 := c.Get(ctx, "v:mt5")
	if loc2 == nil || fetches != 1 {
		t.Errorf("TTL 内第二次 get 应走缓存（fetches=%d）, loc2=%v", fetches, loc2)
	}
	// 失败 → nil 不报错且不缓存正结果（下轮重试）
	boom := errors.New("rpc down")
	c2 := NewServerTZCache(time.Hour, func(_ context.Context, _ string) (float64, error) {
		return 0, boom
	})
	if got := c2.Get(ctx, "v:mt5"); got != nil {
		t.Errorf("fetch fail want nil, got %v", got)
	}
	if got := c2.Get(ctx, "v:mt5"); got != nil {
		t.Errorf("fail 不缓存正结果：second get want nil, got %v", got)
	}
}
