package exec

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
)

// fakeVerifier 测试用 PositionVerifier（D-077 修复⑩ G1）。
type fakeVerifier struct {
	orders map[string][]OpenedOrder // venueKey → 在仓订单
	err    error
}

func (f *fakeVerifier) QueryOpenedOrders(ctx context.Context, venueKey string) ([]OpenedOrder, error) {
	return f.orders[venueKey], f.err
}

// TestCheckResidualLegs_NoResidual 无残留 → 返回空（正常平仓场景）。
func TestCheckResidualLegs_NoResidual(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
		{Phase: "OPEN", Status: "FILLED", Venue: "vB:mt5", OrderID: "2002"},
	}
	// venue 在仓订单不含已平仓 ticket
	v := &fakeVerifier{orders: map[string][]OpenedOrder{
		"vA:mt5": {{Ticket: "9999"}}, // 不含 1001
		"vB:mt5": {{Ticket: "8888"}}, // 不含 2002
	}}
	p := &Pipeline{verifier: v}
	residual, err := p.checkResidualLegs(context.Background(), legs)
	if err != nil {
		t.Fatalf("no residual: unexpected err: %v", err)
	}
	if len(residual) != 0 {
		t.Errorf("no residual want 0, got %v", residual)
	}
}

// TestCheckResidualLegs_HasResidual 有残留 → 返回残留腿描述（孤儿防御场景）。
func TestCheckResidualLegs_HasResidual(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
		{Phase: "OPEN", Status: "FILLED", Venue: "vB:mt5", OrderID: "2002"},
	}
	// vA 的 1001 仍在仓（残留），vB 的 2002 已平
	v := &fakeVerifier{orders: map[string][]OpenedOrder{
		"vA:mt5": {{Ticket: "1001"}, {Ticket: "9999"}}, // 1001 残留
		"vB:mt5": {{Ticket: "8888"}},                   // 不含 2002
	}}
	p := &Pipeline{verifier: v}
	residual, err := p.checkResidualLegs(context.Background(), legs)
	if err != nil {
		t.Fatalf("has residual: unexpected err: %v", err)
	}
	if len(residual) != 1 {
		t.Fatalf("residual want 1, got %d: %v", len(residual), residual)
	}
	if residual[0] != "vA:mt5 ticket=1001" {
		t.Errorf("residual want 'vA:mt5 ticket=1001', got %q", residual[0])
	}
}

// TestCheckResidualLegs_VerifierError verifier 报错 → fail-closed 返回 error（D-108 A2，拒绝置 CLOSED）。
func TestCheckResidualLegs_VerifierError(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
	}
	v := &fakeVerifier{err: context.DeadlineExceeded}
	p := &Pipeline{verifier: v}
	_, err := p.checkResidualLegs(context.Background(), legs)
	if err == nil {
		t.Errorf("verifier error fail-closed want error, got nil")
	}
}

// TestCheckResidualLegs_SkipNonFilledLegs 非 FILLED/非 OPEN phase 腿不查（未成交/平仓腿）。
func TestCheckResidualLegs_SkipNonFilledLegs(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "PENDING", Venue: "vA:mt5", OrderID: "1001"}, // 未成交不查
		{Phase: "CLOSE", Status: "FILLED", Venue: "vB:mt5", OrderID: "2002"}, // 平仓腿不查
		{Phase: "OPEN", Status: "FILLED", Venue: "vC:mt5", OrderID: ""},      // 无 ticket 不查
	}
	v := &fakeVerifier{orders: map[string][]OpenedOrder{}}
	p := &Pipeline{verifier: v}
	residual, err := p.checkResidualLegs(context.Background(), legs)
	if err != nil {
		t.Fatalf("skip: unexpected err: %v", err)
	}
	if len(residual) != 0 {
		t.Errorf("skip non-filled/non-open/empty-ticket want 0, got %v", residual)
	}
}

// TestCheckResidualLegs_NilVerifier verifier 未接线 → 返回 error（D-108 A2 fail-closed）。
func TestCheckResidualLegs_NilVerifier(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
	}
	p := &Pipeline{verifier: nil}
	_, err := p.checkResidualLegs(context.Background(), legs)
	if err == nil {
		t.Errorf("nil verifier fail-closed want error, got nil")
	}
}
