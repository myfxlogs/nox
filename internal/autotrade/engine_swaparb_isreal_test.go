package autotrade

// D-144 ① 三态单测：discoverSwapCombos 同类型配对安全红线。
// 用例①：跨类型组合（实盘×demo）被滤——零跨类型候选。
// 用例②：同类型组合不受影响（demo×demo / 实盘×实盘 均通过）。
// 用例③：未知(NULL)不配对——is_real=NULL 的账户不参与组合（保守）。
// 用例④：纯函数 isRealPair（三态：nil/缺失→不配对）。

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// insertIsRealTestFacts 插入 swap facts + 设置 broker_accounts.is_real 标记（三态）。
// realVenues → is_real=true; demoVenues → is_real=false; nilVenues → is_real=NULL(未知)。
func insertIsRealTestFacts(t *testing.T, s *pgstore.Store, facts []fact.Fact, realVenues, demoVenues, nilVenues []string) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE facts`); err != nil {
		t.Fatalf("truncate facts: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `TRUNCATE broker_accounts`); err != nil {
		t.Fatalf("truncate broker_accounts: %v", err)
	}
	if err := s.InsertFacts(ctx, facts); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	insert := func(v string, isReal interface{}) {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, status, is_real)
			 VALUES ($1, 1, 'h', 443, 1, 'p', 's', 'connected', $2) ON CONFLICT DO NOTHING`, v, isReal); err != nil {
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
	e := &Engine{store: s, cfg: Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
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
	e := &Engine{store: s, cfg: Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
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
	e := &Engine{store: s, cfg: Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
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
