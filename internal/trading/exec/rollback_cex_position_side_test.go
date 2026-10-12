// d249_s4_rollback_posside_test.go —— D-249 S4 批3 第1项：D-201 回滚单补 PositionSide（对齐 placeCloseOrder）。
// 审计 P1-1：pipeline_legs_persist.go:79-85 回滚单不带 PositionSide/reduceOnly，
// 对照 placeCloseOrder（resume_helpers:76-86）与 auto-flatten（legs_flatten:79-92）均显式传。
// long_short_mode 账户上回滚 long 会新开 short——broker 端双倍敞口+本地零记录。
// 修法：回滚单 PlaceOrder 传 PositionSide=原持仓侧（leg.Side 推导，与 placeCloseOrder 一致）。
package exec

import (
	"context"
	"testing"
)

// TestD249_RollbackCEX_PositionSide D-249 S4：CEX 回滚单 PlaceOrder 须带 PositionSide=原持仓侧。
// BUY 腿 → 回滚 SELL + PositionSide=long（减 long 不新开 short）；
// SELL 腿 → 回滚 BUY + PositionSide=short（减 short 不新开 long）。
func TestD249_RollbackCEX_PositionSide(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts CASCADE`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	reg := NewRegistry()
	exe := &fakeRollbackExecutor{venue: "okx:perp"}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s, csResolver: permissiveCSResolver{}} // D-311 R4：:perp 腿回滚/执行换算需要 cs

	legs := []LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001},
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 0.001},
	}
	results := []LegResult{
		{VenueKey: "okx:perp", OrderID: "cex-buy-1", Status: "FILLED", Qty: 0.001, Price: 50000},
		{VenueKey: "okx:perp", OrderID: "cex-sell-2", Status: "FILLED", Qty: 0.001, Price: 50000},
	}
	p.rollbackFilledLegs(ctx, 998, legs, results, 1)

	if exe.placeCount != 2 {
		t.Fatalf("PlaceOrder 调用次数=%d, want 2", exe.placeCount)
	}
	// 第 2 腿 SELL → 回滚 BUY + PositionSide=short（减 short 不新开 long）
	if exe.lastPlaceOrd.Side != "BUY" {
		t.Errorf("第 2 腿 SELL 反向应 BUY, got %s", exe.lastPlaceOrd.Side)
	}
	if exe.lastPlaceOrd.PositionSide != "short" {
		t.Errorf("第 2 腿 SELL 回滚 PositionSide=%q, want short（减 short 不新开 long）", exe.lastPlaceOrd.PositionSide)
	}
}

// TestD249_RollbackCEX_BuyLeg_PositionSide D-249 S4：BUY 腿回滚 → SELL + PositionSide=long。
// 单腿场景验证 BUY 腿回滚的 PositionSide=long（placeCloseOrder 同款推导）。
func TestD249_RollbackCEX_BuyLeg_PositionSide(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts CASCADE`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	reg := NewRegistry()
	exe := &fakeRollbackExecutor{venue: "binance:perp"}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s, csResolver: permissiveCSResolver{}} // D-311 R4：:perp 腿回滚/执行换算需要 cs

	legs := []LegSpec{
		{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "BUY", Qty: 0.002},
	}
	results := []LegResult{
		{VenueKey: "binance:perp", OrderID: "bn-buy-1", Status: "FILLED", Qty: 0.002, Price: 50000},
	}
	p.rollbackFilledLegs(ctx, 995, legs, results, 0)

	if exe.placeCount != 1 {
		t.Fatalf("PlaceOrder 调用次数=%d, want 1", exe.placeCount)
	}
	if exe.lastPlaceOrd.Side != "SELL" {
		t.Errorf("BUY 腿反向应 SELL, got %s", exe.lastPlaceOrd.Side)
	}
	if exe.lastPlaceOrd.PositionSide != "long" {
		t.Errorf("BUY 腿回滚 PositionSide=%q, want long（减 long 不新开 short）", exe.lastPlaceOrd.PositionSide)
	}
}
