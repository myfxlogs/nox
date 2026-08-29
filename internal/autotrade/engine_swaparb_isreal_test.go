package autotrade

// D-144 ① 单测：discoverSwapCombos 同类型配对安全红线。
// 用例①：跨类型组合（实盘×demo）被滤——零跨类型候选。
// 用例②：同类型组合不受影响（demo×demo / 实盘×实盘 均通过）。
// 用例③：is_real 默认 false 兼容存量（无 is_real 行 → 全 demo → 全通过）。
// 用例④：纯函数 isRealPair。

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// insertIsRealTestFacts 插入 swap facts + 设置 broker_accounts.is_real 标记。
func insertIsRealTestFacts(t *testing.T, s *pgstore.Store, facts []fact.Fact, realVenues []string) {
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
	now := time.Now().UTC()
	for _, v := range realVenues {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, status, is_real)
			 VALUES ($1, 1, 'h', 443, 1, 'p', 's', 'connected', true) ON CONFLICT DO NOTHING`, v); err != nil {
			t.Fatalf("insert broker_account %s: %v", v, err)
		}
	}
	// demo venues（is_real=false 默认）——确保有行存在以测试同类型
	demoVenues := []string{"vA", "vB", "vC", "vD"}
	for _, v := range demoVenues {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, platform, host, port, user_id, password, server, status, is_real)
			 VALUES ($1, 1, 'h', 443, 1, 'p', 's', 'connected', false) ON CONFLICT DO NOTHING`, v); err != nil {
			t.Fatalf("insert demo broker_account %s: %v", v, err)
		}
	}
	_ = now
}

// 用例①：跨类型组合（实盘×demo）被滤——零跨类型候选。
func TestDiscoverSwapCombos_CrossTypeFiltered(t *testing.T) {
	s := testAccumStore(t)
	now := time.Now().UTC()
	// vA=实盘 long, vB=demo short → 跨类型，应被滤
	insertIsRealTestFacts(t, s, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}, []string{"vA"})
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
	}, []string{"vC", "vD"})
	e := &Engine{store: s, cfg: Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(context.Background())
	if len(combos) != 2 {
		t.Fatalf("同类型组合应通过（demo×demo + 实盘×实盘），want 2, got %d", len(combos))
	}
}

// 用例③：is_real 默认 false 兼容存量（无 is_real=true 行 → 全 demo → 全通过）。
func TestDiscoverSwapCombos_DefaultFalseCompat(t *testing.T) {
	s := testAccumStore(t)
	now := time.Now().UTC()
	// 无实盘标记 → 全 demo → 同类型 → 通过
	insertIsRealTestFacts(t, s, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "AUDJPY", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "vB", Symbol: "AUDJPY", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}, nil) // 无实盘
	e := &Engine{store: s, cfg: Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(context.Background())
	if len(combos) != 1 {
		t.Errorf("默认全 demo → 同类型 → 应通过，want 1, got %d", len(combos))
	}
}

// 用例④：纯函数 isRealPair
func TestIsRealPair(t *testing.T) {
	m := map[string]bool{"vA": true, "vB": false}
	tests := []struct {
		a, b string
		want bool
	}{
		{"vA", "vA", true},  // 同 venue 同类型
		{"vA", "vB", false}, // 实盘×demo → 滤
		{"vB", "vA", false}, // demo×实盘 → 滤
		{"vB", "vB", true},  // demo×demo → 通过
		{"vC", "vD", true},  // 均不在 map → 默认 false → 同类型 → 通过
		{"vA", "vC", false}, // 实盘×未知(demo) → 滤
	}
	for _, tt := range tests {
		got := isRealPair(m, tt.a, tt.b)
		if got != tt.want {
			t.Errorf("isRealPair(%s,%s) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}
