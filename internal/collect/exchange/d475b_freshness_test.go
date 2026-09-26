// d475b_freshness_test.go —— D-475b S2 T2（WarnUnconsumed warn/dedup/非关键键静默）
// + S3 T3（TTL 超窗回落 REST 断言：posCache 10s / orderCache 5s seam=ttl/now 注入）。
package exchange

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"testing"
	"time"

	"arb/internal/reconcile"
)

// T2a：帧含 upl 且 consumed 缺 → warn 日志；同键 1h 内去重不重复。
func TestD475b_WarnUnconsumed_WarnAndDedup(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	now := time.Unix(1700000000, 0)
	fieldDiffNow = func() time.Time { return now }
	defer func() { fieldDiffNow = time.Now }()
	fieldDiffMu.Lock()
	delete(fieldDiffSeen, "okx.positions.upl")
	fieldDiffMu.Unlock()

	raw := json.RawMessage(`[{"instId":"X","upl":"-1","pos":"1"}]`)
	WarnUnconsumed("okx", "positions", raw, []string{"instId", "pos"})
	if !bytes.Contains(buf.Bytes(), []byte("未消费关键字段: upl")) {
		t.Fatalf("应 warn upl 未消费，log=%s", buf.String())
	}
	// 1h 内重放同键 → 静默（去重）。
	buf.Reset()
	WarnUnconsumed("okx", "positions", raw, []string{"instId", "pos"})
	if buf.Len() != 0 {
		t.Fatalf("1h 去重窗内不应重复告警，log=%s", buf.String())
	}
	// 越窗 → 再告警。
	fieldDiffNow = func() time.Time { return now.Add(2 * time.Hour) }
	WarnUnconsumed("okx", "positions", raw, []string{"instId", "pos"})
	if buf.Len() == 0 {
		t.Fatal("越 1h 窗应再告警")
	}
}

// T2b：非关键键在场零日志；关键键已消费零日志。
func TestD475b_WarnUnconsumed_Quiet(t *testing.T) {
	var buf bytes.Buffer
	old := log.Writer()
	log.SetOutput(&buf)
	defer log.SetOutput(old)
	now := time.Unix(1700000000, 0)
	fieldDiffNow = func() time.Time { return now }
	defer func() { fieldDiffNow = time.Now }()
	fieldDiffMu.Lock()
	fieldDiffSeen = map[string]time.Time{}
	fieldDiffMu.Unlock()

	WarnUnconsumed("okx", "positions", json.RawMessage(`[{"instId":"X","upl":"-1"}]`), []string{"instId", "upl"})
	WarnUnconsumed("okx", "positions", json.RawMessage(`[{"instId":"X","notional_x":"1"}]`), []string{"instId"})
	WarnUnconsumed("okx", "positions", json.RawMessage(`not-json`), nil)
	if buf.Len() != 0 {
		t.Fatalf("已消费/非关键/非 JSON 均不应告警，log=%s", buf.String())
	}
}

// ==== T3 TTL 回落断言（S3：既存 seam ttl/now 注入） ====

type d475bRestPos struct {
	calls  int
	orders []reconcile.VenueOrder
}

func (f *d475bRestPos) VenueKey() string { return "okx:perp" }
func (f *d475bRestPos) QueryOpenedOrders(context.Context) ([]reconcile.VenueOrder, error) {
	f.calls++
	return f.orders, nil
}

type d475bRestOrd struct{ calls int }

func (f *d475bRestOrd) QueryOrderStatus(context.Context, string, string, string) (string, float64, float64, float64, error) {
	f.calls++
	return "filled", 1, 2, 0, nil
}

// 持仓缓存：10s 窗内命中 cache；超窗 → REST 回落。
func TestD475b_PosCache_TTLFallback(t *testing.T) {
	rest := &d475bRestPos{orders: []reconcile.VenueOrder{{Symbol: "REST"}}}
	cache := NewPositionEventCache()
	cache.PutPositions("okx:perp", []reconcile.VenueOrder{{Symbol: "WS"}}, time.Unix(1700000000, 0))
	q := &CachedVenueQuerier{rest: rest, cache: cache, ttl: PositionFreshTTL, now: func() time.Time { return time.Unix(1700000005, 0) }}
	got, _ := q.QueryOpenedOrders(context.Background())
	if rest.calls != 0 || got[0].Symbol != "WS" {
		t.Fatalf("窗内应命中 cache, calls=%d got=%v", rest.calls, got)
	}
	// 超 10s → 回落 REST。
	q.now = func() time.Time { return time.Unix(1700000011, 0) }
	got, _ = q.QueryOpenedOrders(context.Background())
	if rest.calls != 1 || got[0].Symbol != "REST" {
		t.Fatalf("超窗应回落 REST, calls=%d got=%v", rest.calls, got)
	}
}

// 订单缓存：5s 窗内命中；超窗 → REST 回落。
func TestD475b_OrderCache_TTLFallback(t *testing.T) {
	rest := &d475bRestOrd{}
	cache := NewOrderEventCache()
	cache.PutOrder("okx:perp", "ord1", "", OrderEvent{State: "live", At: time.Unix(1700000000, 0)})
	q := &CachedOrderStatusQuerier{venueKey: "okx:perp", rest: rest, cache: cache, ttl: EventFreshTTL, now: func() time.Time { return time.Unix(1700000004, 0) }}
	st, _, _, _, _ := q.QueryOrderStatus(context.Background(), "ord1", "", "X")
	if rest.calls != 0 || st != "live" {
		t.Fatalf("窗内应命中 cache, calls=%d state=%s", rest.calls, st)
	}
	q.now = func() time.Time { return time.Unix(1700000006, 0) }
	st, _, _, _, _ = q.QueryOrderStatus(context.Background(), "ord1", "", "X")
	if rest.calls != 1 || st != "filled" {
		t.Fatalf("超窗应回落 REST, calls=%d state=%s", rest.calls, st)
	}
	var _ OrderStatusQuerier = rest // 接口断言：fake 实现同签名
}
