package dashboard

// D-636 true_net 段缓存锚：①TTL 内直用缓存零重算（池饿根治面）②过期单赢家再刷
// ③刷新败=缓存 stale 保（原「任一读败整段缺」语义的缓存版）。
import (
	"context"
	"errors"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

func TestD636_TrueNetCache(t *testing.T) {
	s := &Service{} // fetch 注入——store 零涉
	calls := 0
	s.tnCache.fetch = func(context.Context, time.Time, time.Time) (*pgstore.TrueNetProfit, error) {
		calls++
		return &pgstore.TrueNetProfit{NetUSD: float64(calls), Covered: []string{"x"}}, nil
	}
	now := time.Now()
	resp1 := &v1.GetOverviewBundleResponse{}
	s.trueNetSeg(resp1, now)
	// 一次刷新=双窗两拉（r30+rall）——锚按「刷新对」计数。
	if calls != 2 || !resp1.TrueNetAvailable || resp1.TrueNetAllUsd != 2 {
		t.Fatalf("首请求应刷一次（两拉）并填值: calls=%d resp=%+v", calls, resp1)
	}
	// ①TTL 内：直用缓存零重算
	resp2 := &v1.GetOverviewBundleResponse{}
	s.trueNetSeg(resp2, now.Add(time.Second))
	if calls != 2 || resp2.TrueNetAllUsd != 2 {
		t.Fatalf("TTL 内不应重算: calls=%d resp2=%+v", calls, resp2)
	}
	// ②过期：单赢家再刷
	resp3 := &v1.GetOverviewBundleResponse{}
	s.trueNetSeg(resp3, now.Add(trueNetCacheTTL+time.Minute))
	if calls != 4 || resp3.TrueNetAllUsd != 4 {
		t.Fatalf("过期应再刷一次: calls=%d resp3=%+v", calls, resp3)
	}
	// ③刷新败=缓存 stale 保（r30 败即返，rall 不发；原值仍在供）
	s.tnCache.fetch = func(context.Context, time.Time, time.Time) (*pgstore.TrueNetProfit, error) {
		calls++
		return nil, errors.New("pool 饿")
	}
	resp4 := &v1.GetOverviewBundleResponse{}
	s.trueNetSeg(resp4, now.Add(2*trueNetCacheTTL+2*time.Minute))
	if calls != 5 || resp4.TrueNetAllUsd != 4 {
		t.Fatalf("刷新败应 stale 保: calls=%d resp4=%+v", calls, resp4)
	}
}
