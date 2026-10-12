// D-282 S5 测试：价格提醒规则引擎（fake store 注入，纯逻辑测试）。
package notify

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
)

// fakePriceAlertStore 注入测试规则，记录调用。
type fakePriceAlertStore struct {
	rules   []PriceAlertRule
	updates []struct {
		id int64
		at time.Time
	}
	listErr error
}

func (f *fakePriceAlertStore) ListEnabledPriceAlerts(ctx context.Context) ([]PriceAlertRule, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.rules, nil
}

func (f *fakePriceAlertStore) UpdatePriceAlertLastAlert(ctx context.Context, id int64, at time.Time) error {
	f.updates = append(f.updates, struct {
		id int64
		at time.Time
	}{id, at})
	return nil
}

// newTestPriceAlertService 构造测试服务（fake store + alert 收集器 + 固定时间）。
func newTestPriceAlertService(t *testing.T, store *fakePriceAlertStore, throttle time.Duration) (*PriceAlertService, *[]string) {
	t.Helper()
	var alerts []string
	svc := NewPriceAlertService(nil, store, func(level, msg string) {
		alerts = append(alerts, level+":"+msg)
	}, throttle)
	t0 := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	svc.SetNow(func() time.Time { return t0 })
	return svc, &alerts
}

// TestRuleMatch 测 >= / <= 命中与未命中，未知 op 不命中。
func TestRuleMatch(t *testing.T) {
	cases := []struct {
		op        string
		threshold float64
		mid       float64
		want      bool
	}{
		{">=", 0.34, 0.34, true},   // 边界命中
		{">=", 0.34, 0.35, true},   // 上行命中
		{">=", 0.34, 0.339, false}, // 未命中
		{"<=", 0.34, 0.34, true},   // 边界命中
		{"<=", 0.34, 0.33, true},   // 下行命中
		{"<=", 0.34, 0.341, false}, // 未命中
		{"==", 0.34, 0.34, false},  // 未知 op
	}
	for i, c := range cases {
		r := &PriceAlertRule{Op: c.op, Threshold: c.threshold}
		if got := ruleMatch(r, c.mid); got != c.want {
			t.Errorf("case %d: op=%s thr=%.4f mid=%.4f got=%v want=%v", i, c.op, c.threshold, c.mid, got, c.want)
		}
	}
}

// TestOnQuote_EdgeTrigger 首次触发告警，持续触发不重复，回落后再触发告警。
func TestOnQuote_EdgeTrigger(t *testing.T) {
	store := &fakePriceAlertStore{
		rules: []PriceAlertRule{{ID: 1, Symbol: "TRXUSDT", Op: ">=", Threshold: 0.34, Level: "warn", Message: "补仓"}},
	}
	svc, alerts := newTestPriceAlertService(t, store, time.Hour)
	ctx := context.Background()
	if err := svc.loadRules(ctx); err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	// 1. 首次未触发（0.33）
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.329, Ask: 0.331})
	if len(*alerts) != 0 {
		t.Fatalf("first non-trigger: got %d alerts, want 0", len(*alerts))
	}
	// 2. 触发（0.34）
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.339, Ask: 0.341})
	if len(*alerts) != 1 {
		t.Fatalf("trigger: got %d alerts, want 1", len(*alerts))
	}
	// 3. 持续触发（0.35）不重复
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.349, Ask: 0.351})
	if len(*alerts) != 1 {
		t.Fatalf("sustained trigger: got %d alerts, want 1", len(*alerts))
	}
	// 4. 回落（0.33）
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.329, Ask: 0.331})
	if len(*alerts) != 1 {
		t.Fatalf("fallback: got %d alerts, want 1", len(*alerts))
	}
	// 5. 再触发（0.34）——节流期内不告警
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.339, Ask: 0.341})
	if len(*alerts) != 1 {
		t.Fatalf("re-trigger in throttle: got %d alerts, want 1", len(*alerts))
	}
}

// TestOnQuote_Throttle 节流期内不告警，节流期满后告警。
func TestOnQuote_Throttle(t *testing.T) {
	store := &fakePriceAlertStore{
		rules: []PriceAlertRule{{ID: 1, Symbol: "TRXUSDT", Op: ">=", Threshold: 0.34, Level: "warn", Message: "补仓"}},
	}
	svc, alerts := newTestPriceAlertService(t, store, 30*time.Minute)
	ctx := context.Background()
	if err := svc.loadRules(ctx); err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	t0 := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	now := t0
	svc.SetNow(func() time.Time { return now })
	// 1. 首次触发
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.339, Ask: 0.341})
	if len(*alerts) != 1 {
		t.Fatalf("first trigger: got %d alerts, want 1", len(*alerts))
	}
	// 2. 回落
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.329, Ask: 0.331})
	// 3. 节流期内再触发（+20min）
	now = t0.Add(20 * time.Minute)
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.339, Ask: 0.341})
	if len(*alerts) != 1 {
		t.Fatalf("throttle period: got %d alerts, want 1", len(*alerts))
	}
	// 4. 节流期满再触发（+31min，先回落再触发）
	now = t0.Add(31 * time.Minute)
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.329, Ask: 0.331}) // 回落
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.339, Ask: 0.341}) // 触发
	if len(*alerts) != 2 {
		t.Fatalf("after throttle: got %d alerts, want 2", len(*alerts))
	}
}

// TestOnQuote_MultiRule 同 symbol 多规则各自独立触发。
func TestOnQuote_MultiRule(t *testing.T) {
	store := &fakePriceAlertStore{
		rules: []PriceAlertRule{
			{ID: 1, Symbol: "TRXUSDT", Op: ">=", Threshold: 0.34, Level: "warn", Message: "上行"},
			{ID: 2, Symbol: "TRXUSDT", Op: "<=", Threshold: 0.30, Level: "warn", Message: "下行"},
		},
	}
	svc, alerts := newTestPriceAlertService(t, store, time.Hour)
	ctx := context.Background()
	if err := svc.loadRules(ctx); err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	// 1. 上行触发（规则1）
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.339, Ask: 0.341})
	if len(*alerts) != 1 {
		t.Fatalf("up trigger: got %d alerts, want 1", len(*alerts))
	}
	// 2. 下行触发（规则2，mid=0.30）
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.299, Ask: 0.301})
	if len(*alerts) != 2 {
		t.Fatalf("down trigger: got %d alerts, want 2", len(*alerts))
	}
}

// TestOnQuote_SymbolFilter 非目标 symbol 报价不触发。
func TestOnQuote_SymbolFilter(t *testing.T) {
	store := &fakePriceAlertStore{
		rules: []PriceAlertRule{{ID: 1, Symbol: "TRXUSDT", Op: ">=", Threshold: 0.34, Level: "warn", Message: "补仓"}},
	}
	svc, alerts := newTestPriceAlertService(t, store, time.Hour)
	ctx := context.Background()
	if err := svc.loadRules(ctx); err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	// BTCUSDT 报价不触发 TRXUSDT 规则
	svc.onQuote(ctx, bus.Quote{Symbol: "BTCUSDT", Bid: 100, Ask: 101})
	if len(*alerts) != 0 {
		t.Fatalf("symbol filter: got %d alerts, want 0", len(*alerts))
	}
}

// TestLoadRules_Refresh 新增/删除规则后刷新缓存。
func TestLoadRules_Refresh(t *testing.T) {
	store := &fakePriceAlertStore{
		rules: []PriceAlertRule{{ID: 1, Symbol: "TRXUSDT", Op: ">=", Threshold: 0.34, Level: "warn", Message: "r1"}},
	}
	svc, _ := newTestPriceAlertService(t, store, time.Hour)
	ctx := context.Background()
	if err := svc.loadRules(ctx); err != nil {
		t.Fatalf("loadRules 1: %v", err)
	}
	if len(svc.rules) != 1 {
		t.Fatalf("after load 1: got %d rules, want 1", len(svc.rules))
	}
	// 删除规则1，新增规则2
	store.rules = []PriceAlertRule{{ID: 2, Symbol: "BTCUSDT", Op: ">=", Threshold: 100000, Level: "warn", Message: "r2"}}
	if err := svc.loadRules(ctx); err != nil {
		t.Fatalf("loadRules 2: %v", err)
	}
	if len(svc.rules) != 1 {
		t.Fatalf("after load 2: got %d rules, want 1", len(svc.rules))
	}
	if _, exists := svc.rules[1]; exists {
		t.Errorf("rule 1 not removed after refresh")
	}
	if _, exists := svc.rules[2]; !exists {
		t.Errorf("rule 2 not added after refresh")
	}
}

// TestLoadRules_LastAlertAt_Persist 重启后 lastAlertAt 从 DB 恢复（节流不重置）。
func TestLoadRules_LastAlertAt_Persist(t *testing.T) {
	lastAlert := time.Date(2026, 9, 11, 11, 50, 0, 0, time.UTC) // 10 分钟前
	store := &fakePriceAlertStore{
		rules: []PriceAlertRule{{
			ID: 1, Symbol: "TRXUSDT", Op: ">=", Threshold: 0.34, Level: "warn", Message: "补仓",
			LastAlertAt: &lastAlert,
		}},
	}
	svc, alerts := newTestPriceAlertService(t, store, 30*time.Minute)
	t0 := time.Date(2026, 9, 11, 12, 0, 0, 0, time.UTC)
	svc.SetNow(func() time.Time { return t0 })
	ctx := context.Background()
	if err := svc.loadRules(ctx); err != nil {
		t.Fatalf("loadRules: %v", err)
	}
	// 节流 30min，上次告警 10min 前 → 还差 20min，触发不应告警
	if svc.lastAlert[1] != lastAlert {
		t.Fatalf("lastAlert not restored: got %v, want %v", svc.lastAlert[1], lastAlert)
	}
	// 触发但节流未满 → 不告警
	svc.onQuote(ctx, bus.Quote{Symbol: "TRXUSDT", Bid: 0.339, Ask: 0.341})
	if len(*alerts) != 0 {
		t.Fatalf("throttle after restore: got %d alerts, want 0", len(*alerts))
	}
}
