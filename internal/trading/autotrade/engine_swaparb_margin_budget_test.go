package autotrade

// D-142 ④ 单测：组合保证金预算观察版告警 + 杠杆不对称提示。
// 用例①：保证金预算超阈值 → warn 告警（不拦截）
// 用例②：保证金预算未超 → 不告警
// 用例③：杠杆不对称 > 2x → info 提示
// 用例④：无限杠杆（2e9）→ 保证金≈0，不告警
// 用例⑤：AlertSink nil → no-op 不 panic
// 用例⑥：纯函数 legMargin / isFiniteLeverage / leverageAsymmetryRatio

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"nox/internal/api/dashboard"
	"nox/internal/data/bus"
	"nox/internal/ledger/reconcile"
	"nox/internal/risk/gate"
	"nox/internal/trading/exec"

	v1 "nox/internal/gen/dashboardv1"
)

// mockAcctQuerier 受控账户查询器（autotrade 测试用，同 dashboard.fakeAccountQuerier）。
type mockAcctQuerier struct {
	key  string
	acct *reconcile.AccountInfo
}

func (f *mockAcctQuerier) VenueKey() string { return f.key }
func (f *mockAcctQuerier) Account(context.Context) (*reconcile.AccountInfo, error) {
	return f.acct, nil
}

// newEngineWithAccts 构造 Engine with mock Service（含 AccountMonitor + 预设账户数据）。
func newEngineWithAccts(t *testing.T, accts ...*mockAcctQuerier) *Engine {
	t.Helper()
	bm := dashboard.NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	topic := bus.NewTopic[*v1.AccountEvent]()
	am := dashboard.NewAccountMonitor(bm, topic)
	for _, q := range accts {
		am.AddStatic(q)
	}
	am.Refresh(context.Background())
	svc := dashboard.New(nil, bus.New(), nil, bm, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, 0, am, nil, 0, 0, nil, 0)
	return &Engine{
		cfg:             gate.Config{SwapArbMarginBudgetPct: 0.8},
		service:         svc,
		marginAlertSent: make(map[string]time.Time), // D-144 ②：去重 map
	}
}

// 用例①：保证金预算超阈值 → warn 告警（不拦截）
func TestObserveMarginBudget_BudgetExceeded(t *testing.T) {
	longQ := &mockAcctQuerier{key: "vA:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vA:mt5", Equity: 1000, Currency: "USD", Leverage: 100,
	}}
	shortQ := &mockAcctQuerier{key: "vB:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vB:mt5", Equity: 1000, Currency: "USD", Leverage: 100,
	}}
	e := newEngineWithAccts(t, longQ, shortQ)
	var alerts []string
	var mu sync.Mutex
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}
	// notional=50000, leverage=100 → margin=500/leg, total=1000, minEq=1000 → 100% > 80%
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 50000)
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, a := range alerts {
		if strings.HasPrefix(a, "warn:") && strings.Contains(a, "保证金占比") {
			found = true
		}
	}
	if !found {
		t.Errorf("应发 warn 告警（保证金预算超阈值），got %d alerts: %v", len(alerts), alerts)
	}
}

// 用例②：保证金预算未超 → 不告警
func TestObserveMarginBudget_BudgetOK(t *testing.T) {
	longQ := &mockAcctQuerier{key: "vA:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vA:mt5", Equity: 10000, Currency: "USD", Leverage: 100,
	}}
	shortQ := &mockAcctQuerier{key: "vB:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vB:mt5", Equity: 10000, Currency: "USD", Leverage: 100,
	}}
	e := newEngineWithAccts(t, longQ, shortQ)
	var alerts []string
	var mu sync.Mutex
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}
	// notional=10000, leverage=100 → margin=100/leg, total=200, minEq=10000 → 2% < 80%
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 10000)
	mu.Lock()
	defer mu.Unlock()
	for _, a := range alerts {
		if strings.Contains(a, "保证金占比") {
			t.Errorf("不应发保证金预算告警（2%% < 80%%），got: %s", a)
		}
	}
}

// 用例③：杠杆不对称 > 2x → info 提示
func TestObserveMarginBudget_LeverageAsymmetry(t *testing.T) {
	longQ := &mockAcctQuerier{key: "vA:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vA:mt5", Equity: 10000, Currency: "USD", Leverage: 100,
	}}
	shortQ := &mockAcctQuerier{key: "vB:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vB:mt5", Equity: 10000, Currency: "USD", Leverage: 500,
	}}
	e := newEngineWithAccts(t, longQ, shortQ)
	var alerts []string
	var mu sync.Mutex
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}
	// 100 vs 500 → 5x 差异 > 2x → info 提示
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 10000)
	mu.Lock()
	defer mu.Unlock()
	found := false
	for _, a := range alerts {
		if strings.HasPrefix(a, "info:") && strings.Contains(a, "杠杆不对称") {
			found = true
		}
	}
	if !found {
		t.Errorf("应发 info 杠杆不对称提示（100 vs 500 = 5x），got %d alerts: %v", len(alerts), alerts)
	}
}

// 用例④：无限杠杆（2e9）→ 保证金≈0，不告警
func TestObserveMarginBudget_InfiniteLeverage(t *testing.T) {
	longQ := &mockAcctQuerier{key: "vA:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vA:mt5", Equity: 1000, Currency: "USD", Leverage: 2e9,
	}}
	shortQ := &mockAcctQuerier{key: "vB:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vB:mt5", Equity: 1000, Currency: "USD", Leverage: 2e9,
	}}
	e := newEngineWithAccts(t, longQ, shortQ)
	var alerts []string
	var mu sync.Mutex
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}
	// 双腿无限杠杆 → 保证金≈0，不告警
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 50000)
	mu.Lock()
	defer mu.Unlock()
	for _, a := range alerts {
		if strings.Contains(a, "保证金占比") {
			t.Errorf("无限杠杆不应发保证金预算告警，got: %s", a)
		}
	}
}

// 用例⑤：AlertSink nil → no-op 不 panic
func TestObserveMarginBudget_NilAlertSink(t *testing.T) {
	e := &Engine{
		cfg: gate.Config{SwapArbMarginBudgetPct: 0.8},
		// AlertSink nil → 函数首行 return，不会调 service
	}
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 10000) // 不应 panic
}

// 用例⑥：纯函数 legMargin
func TestLegMargin(t *testing.T) {
	tests := []struct {
		name     string
		notional float64
		leverage float64
		ok       bool
		want     float64
	}{
		{"normal 100x", 10000, 100, true, 100},
		{"normal 500x", 10000, 500, true, 20},
		{"leverage unknown", 10000, 0, false, 0},
		{"infinite leverage 2e9", 10000, 2e9, true, 0},
		{"zero leverage", 10000, 0, true, 0},
		{"negative leverage", 10000, -100, true, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := legMargin(tt.notional, tt.leverage, tt.ok)
			if got != tt.want {
				t.Errorf("legMargin(%.0f, %.0f, %v) = %.4f, want %.4f", tt.notional, tt.leverage, tt.ok, got, tt.want)
			}
		})
	}
}

// 用例⑥：纯函数 isFiniteLeverage
func TestIsFiniteLeverage(t *testing.T) {
	tests := []struct {
		lev  float64
		want bool
	}{
		{100, true},
		{500, true},
		{1, true},
		{0, false},
		{-1, false},
		{2e9, false},
		{1e9, false},
		{1e9 - 1, true},
	}
	for _, tt := range tests {
		got := isFiniteLeverage(tt.lev)
		if got != tt.want {
			t.Errorf("isFiniteLeverage(%.0f) = %v, want %v", tt.lev, got, tt.want)
		}
	}
}

// 用例⑥：纯函数 leverageAsymmetryRatio
func TestLeverageAsymmetryRatio(t *testing.T) {
	tests := []struct {
		a, b float64
		want float64
	}{
		{100, 100, 1},
		{100, 200, 2},
		{100, 500, 5},
		{500, 100, 5},
		{0, 100, 1},
		{100, 0, 1},
	}
	for _, tt := range tests {
		got := leverageAsymmetryRatio(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("leverageAsymmetryRatio(%.0f, %.0f) = %.4f, want %.4f", tt.a, tt.b, got, tt.want)
		}
	}
}

// D-144 ② 单测：保证金告警 per-key 去重（1h 窗口）。
// 用例⑦：同组合同类型 1h 内第二次调用 → 不重发（budget）
// 用例⑧：同组合不同类型（budget vs asymmetry）→ 独立去重
// 用例⑨：不同组合 → 独立去重
// 用例⑩：窗口过期后 → 重发

// 用例⑦：同组合同类型 1h 内第二次 → 不重发（budget）
func TestObserveMarginBudget_Dedup_SameKeySuppressed(t *testing.T) {
	longQ := &mockAcctQuerier{key: "vA:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vA:mt5", Equity: 1000, Currency: "USD", Leverage: 100,
	}}
	shortQ := &mockAcctQuerier{key: "vB:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vB:mt5", Equity: 1000, Currency: "USD", Leverage: 100,
	}}
	e := newEngineWithAccts(t, longQ, shortQ)
	var alerts []string
	var mu sync.Mutex
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}
	// 第一次：发 warn budget 告警
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 50000)
	// 第二次：同组合同类型，1h 内 → 去重不重发
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 50000)
	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, a := range alerts {
		if strings.Contains(a, "保证金占比") {
			count++
		}
	}
	if count != 1 {
		t.Errorf("同 key 1h 内应去重：want 1 budget alert, got %d (alerts: %v)", count, alerts)
	}
}

// 用例⑧：同组合不同类型（budget vs asymmetry）→ 独立去重
func TestObserveMarginBudget_Dedup_DifferentTypeIndependent(t *testing.T) {
	longQ := &mockAcctQuerier{key: "vA:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vA:mt5", Equity: 500, Currency: "USD", Leverage: 100,
	}}
	shortQ := &mockAcctQuerier{key: "vB:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vB:mt5", Equity: 500, Currency: "USD", Leverage: 500,
	}}
	e := newEngineWithAccts(t, longQ, shortQ)
	var alerts []string
	var mu sync.Mutex
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}
	// 第一次：同时触发 budget warn + asymmetry info（不同 key）
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 50000)
	// 第二次：两个类型各自去重
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 50000)
	mu.Lock()
	defer mu.Unlock()
	budgetCount := 0
	asymCount := 0
	for _, a := range alerts {
		if strings.Contains(a, "保证金占比") {
			budgetCount++
		}
		if strings.Contains(a, "杠杆不对称") {
			asymCount++
		}
	}
	if budgetCount != 1 {
		t.Errorf("budget 应去重：want 1, got %d", budgetCount)
	}
	if asymCount != 1 {
		t.Errorf("asymmetry 应去重：want 1, got %d", asymCount)
	}
}

// 用例⑨：不同组合 → 独立去重
func TestObserveMarginBudget_Dedup_DifferentComboIndependent(t *testing.T) {
	longQ := &mockAcctQuerier{key: "vA:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vA:mt5", Equity: 1000, Currency: "USD", Leverage: 100,
	}}
	shortQ := &mockAcctQuerier{key: "vB:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vB:mt5", Equity: 1000, Currency: "USD", Leverage: 100,
	}}
	e := newEngineWithAccts(t, longQ, shortQ)
	var alerts []string
	var mu sync.Mutex
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}
	// AUDJPY 第一次 → 发
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 50000)
	// EURUSD 第一次 → 也应发（不同 canonical → 不同 key）
	e.observeMarginBudget("EURUSD", "vA:mt5", "vB:mt5", 50000)
	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, a := range alerts {
		if strings.Contains(a, "保证金占比") {
			count++
		}
	}
	if count != 2 {
		t.Errorf("不同组合应独立去重：want 2 budget alerts, got %d (alerts: %v)", count, alerts)
	}
}

// 用例⑩：窗口过期后 → 重发
func TestObserveMarginBudget_Dedup_WindowExpiryRealert(t *testing.T) {
	longQ := &mockAcctQuerier{key: "vA:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vA:mt5", Equity: 1000, Currency: "USD", Leverage: 100,
	}}
	shortQ := &mockAcctQuerier{key: "vB:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "vB:mt5", Equity: 1000, Currency: "USD", Leverage: 100,
	}}
	e := newEngineWithAccts(t, longQ, shortQ)
	var alerts []string
	var mu sync.Mutex
	e.AlertSink = func(level, msg string) {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
	}
	// 第一次 → 发
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 50000)
	// 手动回拨 lastSent 到 61min 前（同 mt-health 测试模式）
	key := "AUDJPY:vA:mt5:vB:mt5:budget"
	e.marginAlertMu.Lock()
	e.marginAlertSent[key] = time.Now().Add(-61 * time.Minute)
	e.marginAlertMu.Unlock()
	// 第二次 → 窗口过期 → 重发
	e.observeMarginBudget("AUDJPY", "vA:mt5", "vB:mt5", 50000)
	mu.Lock()
	defer mu.Unlock()
	count := 0
	for _, a := range alerts {
		if strings.Contains(a, "保证金占比") {
			count++
		}
	}
	if count != 2 {
		t.Errorf("窗口过期应重发：want 2 budget alerts, got %d (alerts: %v)", count, alerts)
	}
}
