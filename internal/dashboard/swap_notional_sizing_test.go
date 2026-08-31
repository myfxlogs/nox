package dashboard

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/reconcile"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// TestSwapArbNotionalSizing D-097 R2 表驱动四 case：
// ① $1570/ratio 0.5/step 0.01 → 过门（sizing $785≥$716）
// ② sizing $300<min $716 → skip
// ③ BareCap cap 生效（notional > minEq×BareCap → cap）
// ④ 离线币种 fail-closed 回归（纯函数层不测，测 swapArbBareCapValue 边界）
func TestSwapArbNotionalSizing(t *testing.T) {
	// R1 取证后 minNotional ≈ $716（0.01 手 × 100000 CS × 113.5 ÷ 150 USDJPY）。
	const minNotional01Lot = 716.0

	cases := []struct {
		name         string
		minEq        float64
		minNotional  float64
		ratio        float64
		bareCap      float64
		leverage     float64
		wantOK       bool
		wantNotional float64
	}{
		{
			name:         "pass_1570_ratio05_step001",
			minEq:        1570.0,
			minNotional:  minNotional01Lot,
			ratio:        0.5,
			bareCap:      1.0,
			leverage:     1.0, // D-174：leverage=1.0 等价旧 D-097 R2 行为
			wantOK:       true,
			wantNotional: 785.0, // 1570×0.5×1=785 ≥ 716, 785 < 1570×1.0×1=1570 不 cap
		},
		{
			name:         "skip_sizing300_lt_min716",
			minEq:        600.0, // 600×0.5×1=300 < 716
			minNotional:  minNotional01Lot,
			ratio:        0.5,
			bareCap:      1.0,
			leverage:     1.0,
			wantOK:       false,
			wantNotional: 0,
		},
		{
			name:         "cap_bareCap05_limits_notional",
			minEq:        10000.0,
			minNotional:  minNotional01Lot,
			ratio:        1.0, // ratio=1 → maxNotional=10000×1×1=10000
			bareCap:      0.5, // bareNotionalLimit=10000×0.5×1=5000 < 10000 → cap to 5000
			leverage:     1.0,
			wantOK:       true,
			wantNotional: 5000.0,
		},
		{
			name:         "cap_bareCap0_no_limit",
			minEq:        50000.0,
			minNotional:  minNotional01Lot,
			ratio:        1.0,
			bareCap:      0, // ≤0 = 不 cap
			leverage:     1.0,
			wantOK:       true,
			wantNotional: 50000.0, // ratio=1, lev=1, no cap
		},
		// D-174 杠杆感知新 case：
		{
			name:         "lev1000_pass_silver_scenario",
			minEq:        1000.0,
			minNotional:  1354.0,
			ratio:        0.05, // 5% 保证金使用率
			bareCap:      1.0,
			leverage:     1000.0, // 1:1000 → maxNotional=1000×0.05×1000=50000 ≥ 1354
			wantOK:       true,
			wantNotional: 50000.0, // 50000 < 1000×1.0×1000=1e6 不 cap
		},
		{
			name:         "lev100_pass",
			minEq:        1000.0,
			minNotional:  1354.0,
			ratio:        0.05,
			bareCap:      1.0,
			leverage:     100.0, // 1:100 → maxNotional=1000×0.05×100=5000 ≥ 1354
			wantOK:       true,
			wantNotional: 5000.0,
		},
		{
			name:         "lev1_skip_no_leverage",
			minEq:        1000.0,
			minNotional:  1354.0,
			ratio:        0.05,
			bareCap:      1.0,
			leverage:     1.0, // 无杠杆 → maxNotional=1000×0.05×1=50 < 1354
			wantOK:       false,
			wantNotional: 0,
		},
		{
			name:         "lev1000_bareCap_leverage_aware",
			minEq:        1000.0,
			minNotional:  1354.0,
			ratio:        1.0,  // maxNotional=1000×1×1000=1e6
			bareCap:      0.05, // bareNotionalLimit=1000×0.05×1000=50000 → cap to 50000
			leverage:     1000.0,
			wantOK:       true,
			wantNotional: 50000.0,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := swapArbNotionalSizing(c.minEq, c.minNotional, c.ratio, c.bareCap, c.leverage)
			if ok != c.wantOK {
				t.Fatalf("ok=%v want %v", ok, c.wantOK)
			}
			if ok && math.Abs(got-c.wantNotional) > 0.01 {
				t.Errorf("notional=%.2f want %.2f", got, c.wantNotional)
			}
		})
	}
}

// TestEffectiveLeverage D-174：两腿较小杠杆纯函数（无限哨兵/未知保守回落）。
func TestEffectiveLeverage(t *testing.T) {
	cases := []struct {
		name     string
		longLev  float64
		shortLev float64
		want     float64
	}{
		{"两腿已知", 1000, 100, 100},
		{"long 无限", 2e9, 100, 100},
		{"short 无限", 100, 2e9, 100},
		{"两腿无限", 2e9, 2e9, 1.0},
		{"两腿未知", 0, 0, 1.0},
		{"long 未知", 0, 100, 100},
		{"short 未知", 100, 0, 100},
	}
	for _, c := range cases {
		if got := effectiveLeverage(c.longLev, c.shortLev); got != c.want {
			t.Errorf("%s: effectiveLeverage(%v, %v)=%v want %v", c.name, c.longLev, c.shortLev, got, c.want)
		}
	}
}

// TestSwapArbBareCapValue 边界：≤0 → 0（不 cap），>0 → 原值。
func TestSwapArbBareCapValue(t *testing.T) {
	cases := []struct {
		in   float64
		want float64
	}{
		{0, 0},
		{-1, 0},
		{1.0, 1.0},
		{0.5, 0.5},
		{2.0, 2.0},
	}
	for _, c := range cases {
		if got := swapArbBareCapValue(c.in); got != c.want {
			t.Errorf("swapArbBareCapValue(%v)=%v want %v", c.in, got, c.want)
		}
	}
}

// --- S2 case④ 回归：SwapArbNotional fail-closed 两分支（venue 离线 + 币种非 USD） ---
// 对拍 docs/SWAPARB小手数放行-施工提示词.md line 21 ④「回归：venue 离线/币种非 USD fail-closed 不变」。
// 纯函数 swapArbNotionalSizing 不含这两分支（在 Service.SwapArbNotional 中），此处补齐 Service 层断言。

// newSwapArbServiceWithAccts 构造带 acctMon 的 Service（两 fake 查询器注入）。
func newSwapArbServiceWithAccts(t *testing.T, qs ...*fakeAccountQuerier) *Service {
	t.Helper()
	bm := NewBrokerManager(nil, bus.New(), exec.NewRegistry(), time.Minute)
	topic := bus.NewTopic[*v1.AccountSnapshot]()
	m := NewAccountMonitor(bm, time.Millisecond, topic)
	for _, q := range qs {
		m.AddStatic(q)
	}
	m.Refresh(context.Background()) // 首轮 poll 填充 snapshot
	return &Service{acctMon: m}
}

// TestSwapArbNotional_FailClosed_VenueOffline case④ 分支①：venue 离线 → ok=false reason 含 "offline"。
func TestSwapArbNotional_FailClosed_VenueOffline(t *testing.T) {
	longQ := &fakeAccountQuerier{key: "ic:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "ic:mt5", Venue: "ic", VenueType: "mt5", AssetType: "cfd",
		Balance: 5000, Equity: 5000, Currency: "USD",
	}}
	shortQ := &fakeAccountQuerier{key: "pxbt:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "pxbt:mt5", Venue: "pxbt", VenueType: "mt5", AssetType: "cfd",
		Balance: 5000, Equity: 5000, Currency: "USD",
	}}
	s := newSwapArbServiceWithAccts(t, longQ, shortQ)

	// 先确认两 venue 在线（baseline）。
	_, _, onlineLong := s.AccountEquityUSD("ic:mt5")
	_, _, onlineShort := s.AccountEquityUSD("pxbt:mt5")
	if !onlineLong || !onlineShort {
		t.Fatalf("baseline: both venues should be online, got long=%v short=%v", onlineLong, onlineShort)
	}

	// short venue 离线 → fail-closed。
	shortQ.err = context.DeadlineExceeded
	s.acctMon.Refresh(context.Background())

	_, ok, reason := s.SwapArbNotional("AUDJPY", "ic:mt5", "pxbt:mt5")
	if ok {
		t.Errorf("venue offline: want ok=false, got true")
	}
	if !strings.Contains(reason, "offline") {
		t.Errorf("venue offline: reason want contain 'offline', got %q", reason)
	}
}

// TestSwapArbNotional_FailClosed_CurrencyNonUSD case④ 分支②：币种非 USD → ok=false reason 含 "not USD"。
func TestSwapArbNotional_FailClosed_CurrencyNonUSD(t *testing.T) {
	longQ := &fakeAccountQuerier{key: "ic:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "ic:mt5", Venue: "ic", VenueType: "mt5", AssetType: "cfd",
		Balance: 5000, Equity: 5000, Currency: "EUR", // 非 USD
	}}
	shortQ := &fakeAccountQuerier{key: "pxbt:mt5", acct: &reconcile.AccountInfo{
		VenueKey: "pxbt:mt5", Venue: "pxbt", VenueType: "mt5", AssetType: "cfd",
		Balance: 5000, Equity: 5000, Currency: "USD",
	}}
	s := newSwapArbServiceWithAccts(t, longQ, shortQ)

	_, ok, reason := s.SwapArbNotional("AUDJPY", "ic:mt5", "pxbt:mt5")
	if ok {
		t.Errorf("currency non-USD: want ok=false, got true")
	}
	if !strings.Contains(reason, "not USD") {
		t.Errorf("currency non-USD: reason want contain 'not USD', got %q", reason)
	}
}
