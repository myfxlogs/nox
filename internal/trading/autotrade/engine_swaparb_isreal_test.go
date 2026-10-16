package autotrade

// 三态单测：discoverSwapCombos 同类型配对安全红线。
// 用例①：跨类型组合（实盘×demo）被滤——零跨类型候选。
// 用例②：同类型组合不受影响（demo×demo / 实盘×实盘 均通过）。
// 用例③：未知(NULL)不配对——is_real=NULL 的账户不参与组合（保守）。
// 用例④：纯函数 isRealPair（三态：nil/缺失→不配对）。

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// insertIsRealTestFacts 插入 swap facts + 设置 broker_accounts.is_real 标记（三态）。
// realVenues → is_real=true; demoVenues → is_real=false; nilVenues → is_real=NULL(未知)。
func insertIsRealTestFacts(t *testing.T, s *pgstore.Store, facts []fact.Fact, realVenues, demoVenues, nilVenues []string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate facts: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `TRUNCATE broker_accounts CASCADE`); err != nil {
		t.Fatalf("truncate broker_accounts: %v", err)
	}
	// D-558：配对闸 fail-closed，fixture canonical（AUDJPY/EURUSD）自带产品类裁定。
	seedTestClass(t, s, "AUDJPY", "fx")
	seedTestClass(t, s, "EURUSD", "fx")
	if err := s.InsertFacts(ctx, facts); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	insert := func(v string, isReal interface{}) {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
			 VALUES ($1, 'mt5', 'h', 443, 'p', 's', 'connected', $2) ON CONFLICT DO NOTHING`, v, isReal); err != nil {
			t.Fatalf("insert broker_account %s: %v", v, err)
		}
	}
	for _, v := range realVenues {
		insert(v, true)
	}
	for _, v := range demoVenues {
		insert(v, false)
	}
	for _, v := range nilVenues {
		insert(v, nil) // NULL=未知
	}
	// D-558 续段：符号归一化须有 DB 映射行——facts 去重后逐 (venue, symbol) 落恒等行。
	seeded := map[string]bool{}
	for _, f := range facts {
		k := f.Venue + "|" + f.Symbol
		if !seeded[k] {
			seeded[k] = true
			seedSwapIdentityMap(t, s, f.Venue, f.Symbol)
		}
	}
}

// 用例①：跨类型组合（实盘×demo）被滤——零跨类型候选。
func TestDiscoverSwapCombos_CrossTypeFiltered(t *testing.T) {
	s := testAccumStore(t)
	now := time.Now().UTC()
	// vA=实盘 long, vB=demo short → 跨类型，应被滤
	insertIsRealTestFacts(t, s, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}, []string{"vA"}, []string{"vB"}, nil)
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(context.Background())
	if len(combos) != 0 {
		t.Errorf("跨类型组合（实盘×demo）应被滤，got %d combos: %+v", len(combos), combos)
	}
}

// 用例②：同类型组合不受影响（demo×demo / 实盘×实盘 均通过）。
func TestDiscoverSwapCombos_SameTypePasses(t *testing.T) {
	s := testAccumStore(t)
	now := time.Now().UTC()
	// vA=demo long, vB=demo short → 同类型 demo×demo，应通过
	// vC=实盘 long, vD=实盘 short → 同类型 实盘×实盘，应通过
	insertIsRealTestFacts(t, s, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "vC", Symbol: "EURUSD", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vD", Symbol: "EURUSD", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}, []string{"vC", "vD"}, []string{"vA", "vB"}, nil)
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(context.Background())
	if len(combos) != 2 {
		t.Fatalf("同类型组合应通过（demo×demo + 实盘×实盘），want 2, got %d", len(combos))
	}
}

// 用例③：未知(NULL)不配对——is_real=NULL 的账户不参与组合（保守）。
func TestDiscoverSwapCombos_UnknownNotPaired(t *testing.T) {
	s := testAccumStore(t)
	now := time.Now().UTC()
	// vA=未知 long, vB=未知 short → 均未知 → 不配对 → 零组合
	insertIsRealTestFacts(t, s, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}, nil, nil, []string{"vA", "vB"})
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(context.Background())
	if len(combos) != 0 {
		t.Errorf("未知(NULL)账户不应配对，got %d combos: %+v", len(combos), combos)
	}
}

// 用例④：纯函数 isRealPair（三态）
func TestIsRealPair(t *testing.T) {
	tr := true
	fl := false
	m := map[string]*bool{"vA": &tr, "vB": &fl, "vE": nil}
	tests := []struct {
		a, b string
		want bool
	}{
		{"vA", "vA", true},  // 同 venue 同实盘
		{"vA", "vB", false}, // 实盘×demo → 滤
		{"vB", "vA", false}, // demo×实盘 → 滤
		{"vB", "vB", true},  // demo×demo → 通过
		{"vC", "vD", false}, // 均不在 map → 缺失 → 不配对（三态保守）
		{"vA", "vC", false}, // 实盘×缺失 → 不配对
		{"vA", "vE", false}, // 实盘×未知(nil) → 不配对
		{"vE", "vE", false}, // 未知×未知 → 不配对
		{"vE", "vB", false}, // 未知×demo → 不配对
	}
	for _, tt := range tests {
		got := isRealPair(m, tt.a, tt.b)
		if got != tt.want {
			t.Errorf("isRealPair(%s,%s) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

// discoverSwapCombos 策略标记——netSwap 换算年化 ≥ CarryMinNetAnnualized → CARRY，否则 SWAPARB。
// 用例①：netSwap=2.0 bps/日 → annualPct=7.3 ≥ 4.0 → strategy="CARRY"
// 用例②：netSwap=0.5 bps/日 → annualPct=1.825 < 4.0 → strategy="SWAPARB"
func TestDiscoverSwapCombos_CarryStrategyTagging(t *testing.T) {
	s := testAccumStore(t)
	now := time.Now().UTC()
	// demo×demo 同类型配对（vA long × vB short）
	insertIsRealTestFacts(t, s, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}, nil, []string{"vA", "vB"}, nil)
	// netSwap=5.0+(-3.0)=2.0 bps/日 → annualPct=2.0*365/100=7.3 ≥ 4.0 → CARRY
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour, CarryMinNetAnnualized: 4.0}}
	combos := e.discoverSwapCombos(context.Background())
	if len(combos) != 1 {
		t.Fatalf("CARRY case: want 1 combo, got %d", len(combos))
	}
	if combos[0].strategy != "CARRY" {
		t.Errorf("CARRY case: netSwap=2.0 → annualPct=7.3 ≥ 4.0, want strategy=CARRY, got %q", combos[0].strategy)
	}
}

// TestDiscoverSwapCombos_SwapArbStrategyTagging D-187：netSwap 换算年化 < CarryMinNetAnnualized → SWAPARB。
func TestDiscoverSwapCombos_SwapArbStrategyTagging(t *testing.T) {
	s := testAccumStore(t)
	now := time.Now().UTC()
	insertIsRealTestFacts(t, s, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 2.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -1.5, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}, nil, []string{"vA", "vB"}, nil)
	// netSwap=2.0+(-1.5)=0.5 bps/日 → annualPct=0.5*365/100=1.825 < 4.0 → SWAPARB
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour, CarryMinNetAnnualized: 4.0}}
	combos := e.discoverSwapCombos(context.Background())
	if len(combos) != 1 {
		t.Fatalf("SWAPARB case: want 1 combo, got %d", len(combos))
	}
	if combos[0].strategy != "SWAPARB" {
		t.Errorf("SWAPARB case: netSwap=0.5 → annualPct=1.825 < 4.0, want strategy=SWAPARB, got %q", combos[0].strategy)
	}
}

// TestDiscoverSwapCombos_CarryThresholdZero D-187：CarryMinNetAnnualized=0 → 全部 SWAPARB（关闭 CARRY 标记）。
func TestDiscoverSwapCombos_CarryThresholdZero(t *testing.T) {
	s := testAccumStore(t)
	now := time.Now().UTC()
	insertIsRealTestFacts(t, s, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}, nil, []string{"vA", "vB"}, nil)
	// CarryMinNetAnnualized=0 → 关闭 CARRY 标记，即使 netSwap=2.0（annualPct=7.3）也标 SWAPARB
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour, CarryMinNetAnnualized: 0}}
	combos := e.discoverSwapCombos(context.Background())
	if len(combos) != 1 {
		t.Fatalf("threshold=0 case: want 1 combo, got %d", len(combos))
	}
	if combos[0].strategy != "SWAPARB" {
		t.Errorf("threshold=0: CARRY marking disabled, want SWAPARB, got %q", combos[0].strategy)
	}
}
