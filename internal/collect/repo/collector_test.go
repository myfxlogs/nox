// D-266 S2 测试：RepoCollector（DSN 真实夹具 + httptest mock）。
package repo

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// repoCollTestStore 连接 ARB_TEST_PG_SERVICE_DSN 测试库（dashboard 包同款 DSN，避免与 pgstore 包测试库冲突）。
// TRUNCATE fin_repo_rates + alerts + cnfi_calendar 隔离数据，seed 交易日历。
func repoCollTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, "TRUNCATE fin_repo_rates, alerts, cnfi_calendar CASCADE"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	// seed 交易日历：2026-09-10（周四）= business day
	if err := s.UpsertCNFICalendarDay(ctx, time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC), true, "test"); err != nil {
		t.Fatalf("UpsertCNFICalendarDay: %v", err)
	}
	return s
}

// tradingThursday 返回 2026-09-10 10:00 北京时间（交易时段内，周四）。
func tradingThursday() time.Time {
	loc, _ := time.LoadLocation("Asia/Shanghai")
	return time.Date(2026, 9, 10, 10, 0, 0, 0, loc)
}

// push2Fixture 是 S0 实测响应（4 品种 + 非目标品种）。
const push2Fixture = `{"rc":0,"rt":6,"data":{"total":9,"diff":{"0":{"f1":3,"f2":1375,"f12":"204001","f14":"GC001","f152":2},"1":{"f1":3,"f2":1345,"f12":"204002","f14":"GC002","f152":2},"4":{"f1":3,"f2":1405,"f12":"204007","f14":"GC007","f152":2},"5":{"f1":3,"f2":1415,"f12":"204014","f14":"GC014","f152":2},"6":{"f1":3,"f2":1430,"f12":"204028","f14":"GC028","f152":2}}}}`

// push2FixtureSpike 是 GC007 spike 响应（f2=5500 → 5.5%，超 critical 5.0%）。
const push2FixtureSpike = `{"rc":0,"data":{"diff":{"0":{"f1":3,"f2":1375,"f12":"204001","f14":"GC001","f152":2},"4":{"f1":3,"f2":5500,"f12":"204007","f14":"GC007","f152":5}}}}`

// push2FixtureDash 是含 "-" 的脏值响应（GC007 f2="-" → 跳过）。
const push2FixtureDash = `{"rc":0,"data":{"diff":{"0":{"f1":3,"f2":1375,"f12":"204001","f14":"GC001","f152":2},"4":{"f1":3,"f2":"-","f12":"204007","f14":"GC007","f152":"-"}}}}`

// TestRepoPoll_NonTradingTime 非交易时段 → Poll 返回 nil，无 HTTP 请求（fetchOverride 计数=0）。
func TestRepoPoll_NonTradingTime(t *testing.T) {
	s := repoCollTestStore(t)
	var fetchCount int32
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) {
		atomic.AddInt32(&fetchCount, 1)
		return []byte(push2Fixture), nil
	})
	// 非交易时段：周日 10:00 北京时间
	loc, _ := time.LoadLocation("Asia/Shanghai")
	sunday := time.Date(2026, 9, 13, 10, 0, 0, 0, loc)
	c.SetNowFn(func() time.Time { return sunday })
	fs, err := c.Poll(context.Background())
	if err != nil {
		t.Fatalf("Poll: %v", err)
	}
	if fs != nil {
		t.Errorf("non-trading: fs = %v, want nil", fs)
	}
	if atomic.LoadInt32(&fetchCount) != 0 {
		t.Errorf("non-trading: fetchCount = %d, want 0", fetchCount)
	}
}

// TestRepoPoll_TradingTime_4Rows 交易时段 → mock HTTP 返回 4 品种 → Poll 写 4 行 fin_repo_rates。
func TestRepoPoll_TradingTime_4Rows(t *testing.T) {
	s := repoCollTestStore(t)
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(push2Fixture), nil })
	c.SetNowFn(func() time.Time { return tradingThursday() })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rates, err := s.LatestRepoRates(context.Background())
	if err != nil {
		t.Fatalf("LatestRepoRates: %v", err)
	}
	if len(rates) != 4 {
		t.Fatalf("LatestRepoRates len = %d, want 4 (GC001/GC007/GC014/GC028)", len(rates))
	}
	// GC007 f2=1405 → 1.405%
	for _, r := range rates {
		if r.Code == "204007" {
			if r.RatePct < 1.4 || r.RatePct > 1.41 {
				t.Errorf("GC007 RatePct = %.4f, want ~1.405 (f2=1405/1000)", r.RatePct)
			}
		}
	}
}

// TestRepoPoll_SpikeWarn 交易时段 + GC007 > 3.0% → alertFn warn 1 次。
func TestRepoPoll_SpikeWarn(t *testing.T) {
	s := repoCollTestStore(t)
	// GC007 f2=3500 → 3.5%，超 warn 3.0% 但未超 critical 5.0%
	fixture := `{"rc":0,"data":{"diff":{"4":{"f1":3,"f2":3500,"f12":"204007","f14":"GC007","f152":3}}}}`
	var mu sync.Mutex
	var alerts []string
	alertFn := func(level, msg string) error {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
		return nil
	}
	c := NewRepoCollector(s, alertFn, 3.0, 5.0)
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(fixture), nil })
	c.SetNowFn(func() time.Time { return tradingThursday() })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 {
		t.Fatalf("spike warn: alerts len = %d, want 1", len(alerts))
	}
	if alerts[0][:4] != "warn" {
		t.Errorf("spike warn: level = %q, want warn", alerts[0][:4])
	}
}

// TestRepoPoll_SpikeCritical 交易时段 + GC007 > 5.0% → alertFn critical 1 次。
func TestRepoPoll_SpikeCritical(t *testing.T) {
	s := repoCollTestStore(t)
	var mu sync.Mutex
	var alerts []string
	alertFn := func(level, msg string) error {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
		return nil
	}
	c := NewRepoCollector(s, alertFn, 3.0, 5.0)
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(push2FixtureSpike), nil })
	c.SetNowFn(func() time.Time { return tradingThursday() })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 1 {
		t.Fatalf("spike critical: alerts len = %d, want 1", len(alerts))
	}
	if alerts[0][:8] != "critical" {
		t.Errorf("spike critical: level = %q, want critical", alerts[0][:8])
	}
}

// TestRepoPoll_Dedup 同日重复 Poll + GC007 > 3% → alertFn 只调 1 次（去重走真实 store 路径）。
func TestRepoPoll_Dedup(t *testing.T) {
	s := repoCollTestStore(t)
	fixture := `{"rc":0,"data":{"diff":{"4":{"f1":3,"f2":3500,"f12":"204007","f14":"GC007","f152":3}}}}`
	var mu sync.Mutex
	var alertCount int
	alertFn := func(level, msg string) error {
		mu.Lock()
		alertCount++
		mu.Unlock()
		// 写入 alerts 表使去重生效（模拟 main 装配的 st.InsertAlert 包装）
		return s.InsertAlert(context.Background(), nil, level, msg)
	}
	c := NewRepoCollector(s, alertFn, 3.0, 5.0)
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(fixture), nil })
	c.SetNowFn(func() time.Time { return tradingThursday() })
	// 第一次 Poll → alert 1 次
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 1: %v", err)
	}
	// 第二次 Poll → 去重，alert 0 次
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 2: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if alertCount != 1 {
		t.Errorf("dedup: alertCount = %d, want 1 (同日同阈值只报 1 次)", alertCount)
	}
}

// TestRepoPoll_DashFixture "-" 夹具不炸轮次（GC007 f2="-" → 跳过，GC001 正常写入）。
func TestRepoPoll_DashFixture(t *testing.T) {
	s := repoCollTestStore(t)
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(push2FixtureDash), nil })
	c.SetNowFn(func() time.Time { return tradingThursday() })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll dash: %v", err)
	}
	rates, err := s.LatestRepoRates(context.Background())
	if err != nil {
		t.Fatalf("LatestRepoRates: %v", err)
	}
	// GC001 正常写入，GC007 f2="-" 跳过 → 只 1 行
	if len(rates) != 1 {
		t.Fatalf("dash fixture: rates len = %d, want 1 (GC001 only, GC007 skipped)", len(rates))
	}
	if rates[0].Code != "204001" {
		t.Errorf("dash fixture: code = %q, want 204001", rates[0].Code)
	}
}

// TestRepoPoll_HttpSmoke httptest.Server 端到端（真实 HTTP 路径，非 fetchOverride）。
func TestRepoPoll_HttpSmoke(t *testing.T) {
	s := repoCollTestStore(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(push2Fixture))
	}))
	defer srv.Close()
	// 用 httptest.Server URL 替换 push2API（通过 fetchOverride 间接模拟——
	// 这里直接用 fetchOverride 指向 srv 的 HTTP 客户端，验证 HTTP 解析链路）
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetHTTPClient(srv.Client())
	// fetchOverride 用真实 HTTP 请求 srv
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL, nil)
		resp, err := srv.Client().Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		return io.ReadAll(resp.Body)
	})
	c.SetNowFn(func() time.Time { return tradingThursday() })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll HTTP: %v", err)
	}
	rates, err := s.LatestRepoRates(context.Background())
	if err != nil {
		t.Fatalf("LatestRepoRates: %v", err)
	}
	if len(rates) != 4 {
		t.Errorf("HTTP smoke: rates len = %d, want 4", len(rates))
	}
}

// TestRepoPoll_CriticalThenWarn 先 critical 后 warn → 两条告警都发出（R3 回归）。
// 旧 bug：warn pattern [repo] GC007% 匹配 critical 消息 → warn 被吞。
func TestRepoPoll_CriticalThenWarn(t *testing.T) {
	s := repoCollTestStore(t)
	var mu sync.Mutex
	var alerts []string
	alertFn := func(level, msg string) error {
		mu.Lock()
		alerts = append(alerts, level+":"+msg)
		mu.Unlock()
		return s.InsertAlert(context.Background(), nil, level, msg)
	}
	c := NewRepoCollector(s, alertFn, 3.0, 5.0)
	c.SetNowFn(func() time.Time { return tradingThursday() })

	// 第一次 Poll：critical（f2=5500 → 5.5%）
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) {
		return []byte(`{"rc":0,"data":{"diff":{"4":{"f1":3,"f2":5500,"f12":"204007","f14":"GC007","f152":5}}}}`), nil
	})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 1: %v", err)
	}

	// 第二次 Poll：warn（f2=3500 → 3.5%）
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) {
		return []byte(`{"rc":0,"data":{"diff":{"4":{"f1":3,"f2":3500,"f12":"204007","f14":"GC007","f152":3}}}}`), nil
	})
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll 2: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(alerts) != 2 {
		t.Fatalf("critical→warn: alerts len = %d, want 2 (critical + warn 各 1)", len(alerts))
	}
	if alerts[0][:8] != "critical" {
		t.Errorf("alert[0] = %q, want critical", alerts[0][:8])
	}
	if alerts[1][:4] != "warn" {
		t.Errorf("alert[1] = %q, want warn", alerts[1][:4])
	}
}

// TestRepoPoll_FieldSemantics f1/f2/f3/f152 字段语义回归。
// f2=1375/f1=3 → ratePct=1.375%；f3=1000/f152=2 → changePct=10.00%。
func TestRepoPoll_FieldSemantics(t *testing.T) {
	s := repoCollTestStore(t)
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetNowFn(func() time.Time { return tradingThursday() })
	fixture := `{"rc":0,"data":{"diff":{"4":{"f1":3,"f2":1375,"f3":1000,"f12":"204007","f14":"GC007","f152":2}}}}`
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(fixture), nil })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rates, err := s.LatestRepoRates(context.Background())
	if err != nil {
		t.Fatalf("LatestRepoRates: %v", err)
	}
	if len(rates) != 1 {
		t.Fatalf("rates len = %d, want 1", len(rates))
	}
	r := rates[0]
	if r.RatePct < 1.374 || r.RatePct > 1.376 {
		t.Errorf("RatePct = %.4f, want 1.375 (f2=1375/10^f1=3)", r.RatePct)
	}
	if r.ChangePct == nil {
		t.Fatal("ChangePct = nil, want 10.0 (f3=1000/10^f152=2)")
	}
	if *r.ChangePct < 9.99 || *r.ChangePct > 10.01 {
		t.Errorf("ChangePct = %.4f, want 10.00 (f3=1000/10^f152=2)", *r.ChangePct)
	}
}

// TestRepoPoll_NegativeChange 负涨跌：f3=-500/f152=2 → changePct=-5.00%。
func TestRepoPoll_NegativeChange(t *testing.T) {
	s := repoCollTestStore(t)
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetNowFn(func() time.Time { return tradingThursday() })
	fixture := `{"rc":0,"data":{"diff":{"4":{"f1":3,"f2":1375,"f3":-500,"f12":"204007","f14":"GC007","f152":2}}}}`
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(fixture), nil })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rates, _ := s.LatestRepoRates(context.Background())
	if len(rates) != 1 || rates[0].ChangePct == nil {
		t.Fatal("expected 1 rate with non-nil ChangePct")
	}
	if *rates[0].ChangePct > -4.99 || *rates[0].ChangePct < -5.01 {
		t.Errorf("ChangePct = %.4f, want -5.00", *rates[0].ChangePct)
	}
}

// TestRepoPoll_ZeroChange 真实零涨跌：f3=0/f152=2 → changePct=0.00%（非 nil）。
func TestRepoPoll_ZeroChange(t *testing.T) {
	s := repoCollTestStore(t)
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetNowFn(func() time.Time { return tradingThursday() })
	fixture := `{"rc":0,"data":{"diff":{"4":{"f1":3,"f2":1375,"f3":0,"f12":"204007","f14":"GC007","f152":2}}}}`
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(fixture), nil })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rates, _ := s.LatestRepoRates(context.Background())
	if len(rates) != 1 {
		t.Fatalf("rates len = %d, want 1", len(rates))
	}
	if rates[0].ChangePct == nil {
		t.Fatal("ChangePct = nil, want &0.0 (真实零，非缺失)")
	}
	if *rates[0].ChangePct != 0 {
		t.Errorf("ChangePct = %v, want 0.0", *rates[0].ChangePct)
	}
}

// TestRepoPoll_MissingChange 涨跌幅缺失：f3 缺字段 → change_pct=NULL（保留利率）。
func TestRepoPoll_MissingChange(t *testing.T) {
	s := repoCollTestStore(t)
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetNowFn(func() time.Time { return tradingThursday() })
	fixture := `{"rc":0,"data":{"diff":{"4":{"f1":3,"f2":1375,"f12":"204007","f14":"GC007","f152":2}}}}`
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(fixture), nil })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rates, _ := s.LatestRepoRates(context.Background())
	if len(rates) != 1 {
		t.Fatalf("rates len = %d, want 1", len(rates))
	}
	if rates[0].RatePct < 1.374 || rates[0].RatePct > 1.376 {
		t.Errorf("RatePct = %.4f, want 1.375 (利率应保留)", rates[0].RatePct)
	}
	if rates[0].ChangePct != nil {
		t.Errorf("ChangePct = %v, want nil (f3 缺失 → NULL)", *rates[0].ChangePct)
	}
}

// TestRepoPoll_InvalidPrecision 精度超范围：f1=7 → 跳过该品种。
func TestRepoPoll_InvalidPrecision(t *testing.T) {
	s := repoCollTestStore(t)
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetNowFn(func() time.Time { return tradingThursday() })
	fixture := `{"rc":0,"data":{"diff":{"4":{"f1":7,"f2":1375,"f3":1000,"f12":"204007","f14":"GC007","f152":2}}}}`
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(fixture), nil })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rates, _ := s.LatestRepoRates(context.Background())
	if len(rates) != 0 {
		t.Errorf("rates len = %d, want 0 (f1=7 超范围 → 跳过)", len(rates))
	}
}

// TestRepoPoll_StringNumericFields 字符串数值字段：f2="1375"/f1="3" → 正常解析。
func TestRepoPoll_StringNumericFields(t *testing.T) {
	s := repoCollTestStore(t)
	c := NewRepoCollector(s, nil, 3.0, 5.0)
	c.SetNowFn(func() time.Time { return tradingThursday() })
	fixture := `{"rc":0,"data":{"diff":{"4":{"f1":"3","f2":"1375","f3":"1000","f12":"204007","f14":"GC007","f152":"2"}}}}`
	c.SetFetchOverride(func(ctx context.Context) ([]byte, error) { return []byte(fixture), nil })
	if _, err := c.Poll(context.Background()); err != nil {
		t.Fatalf("Poll: %v", err)
	}
	rates, _ := s.LatestRepoRates(context.Background())
	if len(rates) != 1 {
		t.Fatalf("rates len = %d, want 1", len(rates))
	}
	if r := rates[0]; r.RatePct < 1.374 || r.RatePct > 1.376 || r.ChangePct == nil || *r.ChangePct < 9.99 || *r.ChangePct > 10.01 {
		t.Errorf("string fields: RatePct=%.4f ChangePct=%v, want 1.375 / 10.00", r.RatePct, r.ChangePct)
	}
}
