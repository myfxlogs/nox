package position

import (
	"context"
	"sync"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/store/pgstore"
)

func f64ptr(v float64) *float64 { return &v }

// TestPersonalLevel mid 与 warn/crit 阈值映射（含仅设 crit、两阈值未设、crit < warn 的防御）。
func TestPersonalLevel(t *testing.T) {
	cases := []struct {
		name     string
		mid      float64
		warnHigh *float64
		critHigh *float64
		want     string
	}{
		{name: "below warn", mid: 0.35, warnHigh: f64ptr(0.36), critHigh: f64ptr(0.3695), want: "ok"},
		{name: "at warn", mid: 0.36, warnHigh: f64ptr(0.36), critHigh: f64ptr(0.3695), want: "warn"},
		{name: "above warn below crit", mid: 0.365, warnHigh: f64ptr(0.36), critHigh: f64ptr(0.3695), want: "warn"},
		{name: "at crit", mid: 0.3695, warnHigh: f64ptr(0.36), critHigh: f64ptr(0.3695), want: "crit"},
		{name: "above crit", mid: 0.38, warnHigh: f64ptr(0.36), critHigh: f64ptr(0.3695), want: "crit"},
		{name: "only crit below", mid: 0.35, warnHigh: nil, critHigh: f64ptr(0.3695), want: "ok"},
		{name: "only crit above", mid: 0.37, warnHigh: nil, critHigh: f64ptr(0.3695), want: "crit"},
		{name: "neither set", mid: 0.5, warnHigh: nil, critHigh: nil, want: "ok"},
		{name: "crit below warn defensive", mid: 0.33, warnHigh: f64ptr(0.36), critHigh: f64ptr(0.30), want: "crit"},
		{name: "crit below warn above both", mid: 0.38, warnHigh: f64ptr(0.36), critHigh: f64ptr(0.30), want: "crit"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := PersonalLevel(c.mid, c.warnHigh, c.critHigh); got != c.want {
				t.Errorf("PersonalLevel(mid=%v) = %q, want %q", c.mid, got, c.want)
			}
		})
	}
}

// testMonitor 构造测试用 PersonalMonitor：注入 alertFn 记录告警 + 可控时钟。
func testMonitor(now *time.Time) (*PersonalMonitor, *[]struct{ level, msg string }) {
	var mu sync.Mutex
	var alerts []struct{ level, msg string }
	m := &PersonalMonitor{
		positions: make(map[int64]*pgstore.PersonalPosition),
		level:     make(map[int64]string),
		lastAlert: make(map[int64]time.Time),
		throttle:  5 * time.Second,
		now:       func() time.Time { return *now },
		alertFn: func(_ context.Context, level, msg string) error {
			mu.Lock()
			alerts = append(alerts, struct{ level, msg string }{level, msg})
			mu.Unlock()
			return nil
		},
	}
	return m, &alerts
}

func quote(mid float64) bus.Quote {
	return bus.Quote{Symbol: "TRXUSDT", Bid: mid, Ask: mid}
}

// TestPersonalMonitorEdgeTrigger 边缘触发状态机：ok→warn 发一次、warn→crit 升级、
// 回落不重复、再升重新触发（时钟推进越过节流）。
func TestPersonalMonitorEdgeTrigger(t *testing.T) {
	now := time.Unix(1000, 0)
	m, alerts := testMonitor(&now)
	warnHigh := 0.36
	critHigh := 0.3695
	m.positions[1] = &pgstore.PersonalPosition{
		ID: 1, Asset: "TRX", Kind: "short_hedge", PriceSymbol: "TRXUSDT",
		WarnHigh: &warnHigh, CritHigh: &critHigh,
	}
	ctx := context.Background()

	// ok → warn：发一次
	m.onQuote(ctx, quote(0.361))
	if len(*alerts) != 1 || (*alerts)[0].level != "warn" {
		t.Fatalf("ok→warn want 1 warn alert, got %+v", *alerts)
	}
	// warn 持续：不重发
	m.onQuote(ctx, quote(0.362))
	if len(*alerts) != 1 {
		t.Fatalf("warn persist want no new alert, got %d", len(*alerts))
	}
	// warn → crit：升级（推进 5s 越过节流）
	now = now.Add(5 * time.Second)
	m.onQuote(ctx, quote(0.37))
	if len(*alerts) != 2 || (*alerts)[1].level != "critical" {
		t.Fatalf("warn→crit want 1 critical alert, got %+v", *alerts)
	}
	// crit 回落 ok（更安全）：不告警
	now = now.Add(5 * time.Second)
	m.onQuote(ctx, quote(0.35))
	if len(*alerts) != 2 {
		t.Fatalf("crit→ok want no alert, got %d", len(*alerts))
	}
	// 再升 warn：重新触发
	now = now.Add(5 * time.Second)
	m.onQuote(ctx, quote(0.361))
	if len(*alerts) != 3 || (*alerts)[2].level != "warn" {
		t.Fatalf("re-rise ok→warn want 1 warn alert, got %+v", *alerts)
	}
	// ok → crit 直接（跨级，同样告警）
	now = now.Add(5 * time.Second)
	m.onQuote(ctx, quote(0.38))
	if len(*alerts) != 4 || (*alerts)[3].level != "critical" {
		t.Fatalf("ok→crit want 1 critical alert, got %+v", *alerts)
	}
}

// TestPersonalMonitorThrottle 5s 节流兜底：回落再升在节流窗口内不重发，越过窗口重新触发。
func TestPersonalMonitorThrottle(t *testing.T) {
	now := time.Unix(2000, 0)
	m, alerts := testMonitor(&now)
	warnHigh := 0.36
	m.positions[1] = &pgstore.PersonalPosition{
		ID: 1, Asset: "TRX", Kind: "short_hedge", PriceSymbol: "TRXUSDT",
		WarnHigh: &warnHigh, CritHigh: nil,
	}
	ctx := context.Background()

	// ok → warn：发一次
	m.onQuote(ctx, quote(0.361))
	if len(*alerts) != 1 {
		t.Fatalf("ok→warn want 1 alert, got %d", len(*alerts))
	}
	// warn → ok 回落 → 节流窗口内再升：不重发（5s 节流兜底边界抖动）
	m.onQuote(ctx, quote(0.35))
	m.onQuote(ctx, quote(0.361))
	if len(*alerts) != 1 {
		t.Fatalf("re-rise within throttle want no alert, got %d", len(*alerts))
	}
	// 推进 5s 后回落再升：重新触发
	now = now.Add(5 * time.Second)
	m.onQuote(ctx, quote(0.35))
	m.onQuote(ctx, quote(0.361))
	if len(*alerts) != 2 {
		t.Fatalf("re-rise after throttle want 1 alert, got %d", len(*alerts))
	}
}

// TestPersonalMonitorSymbolFilter 只处理 price_symbol 匹配的报价（其他 canonical 报价不触发）。
func TestPersonalMonitorSymbolFilter(t *testing.T) {
	now := time.Unix(3000, 0)
	m, alerts := testMonitor(&now)
	warnHigh := 0.36
	m.positions[1] = &pgstore.PersonalPosition{
		ID: 1, Asset: "TRX", Kind: "short_hedge", PriceSymbol: "TRXUSDT",
		WarnHigh: &warnHigh, CritHigh: nil,
	}
	ctx := context.Background()

	m.onQuote(ctx, bus.Quote{Symbol: "BTCUSDT", Bid: 0.99, Ask: 0.99})
	m.onQuote(ctx, bus.Quote{Symbol: "ETHUSDT", Bid: 0.99, Ask: 0.99})
	if len(*alerts) != 0 {
		t.Fatalf("unrelated symbols want no alert, got %+v", *alerts)
	}
	m.onQuote(ctx, quote(0.361))
	if len(*alerts) != 1 {
		t.Fatalf("matching symbol want 1 alert, got %d", len(*alerts))
	}
}
