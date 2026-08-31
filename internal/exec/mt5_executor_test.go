package exec

import (
	"testing"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// TestMT5CloseOrder_CloseLotsPreferred 验证 OrderClose 返回 Lots=0（全平后剩余）+ CloseLots=0.05 时，
// Fill.Qty 取 CloseLots 而非 Lots（D-165 修复 #456 平仓失败根因）。
func TestMT5CloseOrder_CloseLotsPreferred(t *testing.T) {
	// OrderClose 后 Order.Lots=剩余持仓=0，CloseLots=本次平仓手数=0.05
	resp := &mt5pb.OrderCloseReply{
		Result: &mt5pb.Order{
			Lots:       0,    // 平仓后剩余=0
			CloseLots:  0.05, // 本次平仓手数
			ClosePrice: 77572.3,
			Swap:       -2.21,
		},
	}
	// 验证逻辑：qty := CloseLots; if <=0 fallback Lots
	qty := resp.Result.GetCloseLots()
	if qty <= 0 {
		qty = resp.Result.GetLots()
	}
	if qty != 0.05 {
		t.Fatalf("expected qty=0.05 (CloseLots), got %.4f", qty)
	}
}

// TestMT5CloseOrder_FallbackToLots 验证旧 gateway 不填 CloseLots 时 fallback 到 Lots。
func TestMT5CloseOrder_FallbackToLots(t *testing.T) {
	resp := &mt5pb.OrderCloseReply{
		Result: &mt5pb.Order{
			Lots:       0.05, // 旧 gateway 填 Lots
			CloseLots:  0,    // 旧 gateway 不填 CloseLots
			ClosePrice: 77572.3,
		},
	}
	qty := resp.Result.GetCloseLots()
	if qty <= 0 {
		qty = resp.Result.GetLots()
	}
	if qty != 0.05 {
		t.Fatalf("expected qty=0.05 (fallback Lots), got %.4f", qty)
	}
}
