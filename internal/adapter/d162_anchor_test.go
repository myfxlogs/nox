package adapter

// D-162R3 锚：CompositeTickAnchor 三级链——
// ① 原生 lastQuote hit → 短路；② 原生 miss → persisted 原生 ticks；
// ③ 原生两级 miss → canonical 归一（XAUUSDm→XAUUSD）同两级；
// ④ canonical 解不出 → false 保留原值不猜。

import (
	"context"
	"testing"
	"time"
)

type d162Persist struct {
	rows  map[string][3]any // sym → {bid,ask,ts}
	calls []string
}

func (p *d162Persist) get(_ context.Context, sym string) (float64, float64, time.Time, bool) {
	p.calls = append(p.calls, sym)
	r, ok := p.rows[sym]
	if !ok {
		return 0, 0, time.Time{}, false
	}
	return r[0].(float64), r[1].(float64), r[2].(time.Time), true
}

func TestD162R3_PriceAnchorChain(t *testing.T) {
	friTs := time.Now().Add(-24 * time.Hour)
	mem := func(_ context.Context, sym string) (float64, float64, time.Time, bool) {
		if sym == "MEMOK" {
			return 1.0, 1.1, friTs, true
		}
		return 0, 0, time.Time{}, false
	}
	p := &d162Persist{rows: map[string][3]any{
		"NATIVE-ONLY": {2.0, 2.1, friTs},
		"XAUUSD":      {4194.85, 4195.11, friTs},
	}}
	canon := map[string]string{"XAUUSDm": "XAUUSD"}
	anchor := CompositeTickAnchor(mem, p.get, canon)
	ctx := context.Background()

	// ① 内存 hit 短路（persist 不查）
	b, _, ts, ok := anchor(ctx, "MEMOK")
	if !ok || b != 1.0 || !ts.Equal(friTs) || len(p.calls) != 0 {
		t.Fatalf("mem hit want (1.0,frTs,no persist), got %v %v calls=%v", b, ts, p.calls)
	}
	// ② 原生 persisted
	b, _, _, ok = anchor(ctx, "NATIVE-ONLY")
	if !ok || b != 2.0 {
		t.Fatalf("native persist want 2.0, got %v", b)
	}
	// ③ 原生 miss → canonical XAUUSD 锚中
	b, a, ts, ok := anchor(ctx, "XAUUSDm")
	if !ok || b != 4194.85 || a != 4195.11 || !ts.Equal(friTs) {
		t.Fatalf("canonical want 4194.85/4195.11@frTs, got %v/%v@%v", b, a, ts)
	}
	// ④ 解不出 → false（不猜）
	if _, _, _, ok = anchor(ctx, "EURUSDm"); ok {
		t.Fatal("unmapped want false")
	}
	if _, _, _, ok = anchor(ctx, "ZZZ"); ok {
		t.Fatal("all-miss want false")
	}
}
