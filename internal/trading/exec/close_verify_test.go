package exec

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"nox/internal/store/pgstore"
)

// fakeVerifier 测试用 PositionVerifier（D-077 修复⑩ G1）。
// drainAfter>0：每 venue 前 drainAfter 次查询返回 orders、之后返回空——
// 建模「平仓后 venue 已无该腿」（D-417：CEX 腿按 Symbol+Side 匹配残余，
// 静态 orders 会被残余核验如实拦下）。
type fakeVerifier struct {
	orders     map[string][]OpenedOrder // venueKey → 在仓订单
	err        error
	drainAfter int
	mu         sync.Mutex
	calls      map[string]int
}

func (f *fakeVerifier) QueryOpenedOrders(ctx context.Context, venueKey string) ([]OpenedOrder, error) {
	if f.drainAfter > 0 {
		f.mu.Lock()
		if f.calls == nil {
			f.calls = map[string]int{}
		}
		f.calls[venueKey]++
		drain := f.calls[venueKey] > f.drainAfter
		f.mu.Unlock()
		if drain {
			return nil, f.err
		}
	}
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

// TestD120_CEXVenueFailClosed D-417：CEX venue verifier 报错 → fail-closed 返 err
// （D-120 豁免已废——CEXPositionQuerier D-121 起在册，查询失败与 MT 同权）。
func TestD120_CEXVenueFailClosed(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", OrderID: "3869140637289013248"},
		{Phase: "OPEN", Status: "FILLED", Venue: "binance:perp", OrderID: "16774225215"},
	}
	v := &fakeVerifier{err: fmt.Errorf("venue 503")}
	p := &Pipeline{verifier: v}
	if _, err := p.checkResidualLegs(context.Background(), legs); err == nil {
		t.Fatal("CEX venue query err must fail-closed, got nil")
	}
}

// TestD120_MTVenueStillFailClosed MT venue verifier 报错 → 仍 fail-closed（D-120 回归保护）。
func TestD120_MTVenueStillFailClosed(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "ic52993526:mt5", OrderID: "1001"},
	}
	v := &fakeVerifier{err: context.DeadlineExceeded}
	p := &Pipeline{verifier: v}
	_, err := p.checkResidualLegs(context.Background(), legs)
	if err == nil {
		t.Errorf("MT venue fail-closed: want error, got nil")
	}
}
