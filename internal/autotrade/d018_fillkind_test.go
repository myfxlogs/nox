// d018_fillkind_test.go —— D-018 后段①：ExitCostEst 按腿 fill_kind 选 maker/taker 费率。
// maker 腿取 maker 费率；maker 源缺（未接线/无行）回落 taker 保守；taker 本身缺 fail-closed。
package autotrade

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
)

func d018MakerStr(s string) *string { return &s }

func TestD018_ExitFeeByFillKind(t *testing.T) {
	ctx := context.Background()
	leg := func(fk *string) pgstore.PositionLeg {
		return pgstore.PositionLeg{Venue: "okx:perp", FillKind: fk}
	}
	eng := func(makerOK bool) *Engine {
		return &Engine{cfg: Config{
			EconFeeBps: func(_ context.Context, vk string) (float64, bool) {
				if vk == "okx:perp" {
					return 5.0, true
				}
				return 0, false
			},
			EconMakerFeeBps: func(_ context.Context, vk string) (float64, bool) {
				if makerOK && vk == "okx:perp" {
					return 2.0, true
				}
				return 0, false
			},
		}}
	}
	if bps, ok := eng(true).econExitFeeBps(ctx, leg(d018MakerStr("maker"))); !ok || bps != 2.0 {
		t.Fatalf("maker 腿应取 maker 费率 2bps，got %v ok=%v", bps, ok)
	}
	if bps, ok := eng(true).econExitFeeBps(ctx, leg(d018MakerStr("taker"))); !ok || bps != 5.0 {
		t.Fatalf("taker 腿应取 taker 费率 5bps，got %v ok=%v", bps, ok)
	}
	if bps, ok := eng(true).econExitFeeBps(ctx, leg(nil)); !ok || bps != 5.0 {
		t.Fatalf("fill_kind NULL（旧数据）应 taker 5bps，got %v ok=%v", bps, ok)
	}
	if bps, ok := eng(false).econExitFeeBps(ctx, leg(d018MakerStr("maker"))); !ok || bps != 5.0 {
		t.Fatalf("maker 源缺应回落 taker 5bps，got %v ok=%v", bps, ok)
	}
	noMaker := &Engine{cfg: Config{EconFeeBps: func(_ context.Context, vk string) (float64, bool) { return 5.0, true }}}
	if bps, ok := noMaker.econExitFeeBps(ctx, leg(d018MakerStr("maker"))); !ok || bps != 5.0 {
		t.Fatalf("EconMakerFeeBps 未接线应回落 taker，got %v ok=%v", bps, ok)
	}
	noTaker := &Engine{cfg: Config{EconFeeBps: func(_ context.Context, _ string) (float64, bool) { return 0, false }}}
	if _, ok := noTaker.econExitFeeBps(ctx, leg(d018MakerStr("maker"))); ok {
		t.Fatal("taker 费率缺须 fail-closed（ok=false）")
	}
}
