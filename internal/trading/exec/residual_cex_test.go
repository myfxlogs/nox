// d417_residual_test.go —— D-417 S1：CEX 腿残腿核验修复测试（先红后绿）。
// CEX leg.OrderID=ordId 而 OpenedOrder.Ticket=instId——ticket 匹配永不命中=死代码；
// 匹配器改 LegAtVenue（CEX=Symbol+Side+PosSide）。另 CEX 查询失败改 fail-closed。
package exec

import (
	"context"
	"fmt"
	"testing"

	"nox/internal/store/pgstore"
)

// TestD417_ResidualCEXL leg CEX 残余检测：leg ordId 与 orders instId 异构但同仓 → 须报残余。
func TestD417_ResidualCEX(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", OrderID: "3947142706953965568",
			Symbol: "XAUT-USDT-SWAP", Side: "BUY"},
	}
	v := &fakeVerifier{orders: map[string][]OpenedOrder{
		// CEX querier 返回 Ticket=instId（与 leg.OrderID=ordId 永不相同）。
		"okx:perp": {{Ticket: "XAUT-USDT-SWAP", Symbol: "XAUT-USDT-SWAP", Side: "BUY", PosSide: "long", Volume: 10}},
	}}
	p := &Pipeline{verifier: v}
	residual, err := p.checkResidualLegs(context.Background(), legs)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(residual) != 1 {
		t.Fatalf("CEX residual want 1, got %d: %v", len(residual), residual)
	}
}

// TestD417_ResidualCEXPosSideMismatch CEX 同 symbol 反向仓非本腿 → 不误报残余。
func TestD417_ResidualCEXPosSideMismatch(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", OrderID: "3947",
			Symbol: "XAUT-USDT-SWAP", Side: "SELL"},
	}
	v := &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:perp": {{Ticket: "XAUT-USDT-SWAP", Symbol: "XAUT-USDT-SWAP", Side: "BUY", PosSide: "long"}},
	}}
	p := &Pipeline{verifier: v}
	residual, err := p.checkResidualLegs(context.Background(), legs)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(residual) != 0 {
		t.Fatalf("posSide mismatch want 0, got %v", residual)
	}
}

// TestD417_ResidualCEXQueryErr CEX QueryOpenedOrders 出错 → fail-closed 返 err
// （旧行为 continue 跳过=真实查询失败 fail-open 漏检）。
func TestD417_ResidualCEXQueryErr(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "gate:perp", OrderID: "888",
			Symbol: "XAUT-USDT", Side: "BUY"},
	}
	v := &fakeVerifier{err: fmt.Errorf("gate 503")}
	p := &Pipeline{verifier: v}
	if _, err := p.checkResidualLegs(context.Background(), legs); err == nil {
		t.Fatal("CEX query err must fail-closed return err, got nil")
	}
}

// TestD417_ResidualGateVenue 第二 CEX venue（gate）同覆盖：残余检出。
func TestD417_ResidualGateVenue(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "gate:perp", OrderID: "777",
			Symbol: "XAU_USDT", Side: "SELL"},
	}
	v := &fakeVerifier{orders: map[string][]OpenedOrder{
		"gate:perp": {{Ticket: "XAU_USDT", Symbol: "XAU_USDT", Side: "SELL", PosSide: "short", Volume: 1}},
	}}
	p := &Pipeline{verifier: v}
	residual, err := p.checkResidualLegs(context.Background(), legs)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(residual) != 1 {
		t.Fatalf("gate residual want 1, got %v", residual)
	}
}
