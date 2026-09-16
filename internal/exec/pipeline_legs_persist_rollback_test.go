// pipeline_legs_persist_rollback_test.go —— D-201 S2：persistOpenLegs 失败补偿回滚测试。
// 四场景：MT CloseOrder / CEX PlaceOrder 反向 / 跳过 UNKNOWN / best-effort 不阻断。
// 直接调 rollbackFilledLegs（单元测试），fake executor 记录调用，real testStore 供 InsertAlert。
package exec

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
)

// fakeRollbackExecutor D-201：记录 CloseOrder/PlaceOrder 调用，可注入 per-leg 错误。
type fakeRollbackExecutor struct {
	venue        string
	closeCount   int32
	placeCount   int32
	lastCloseRef OrderRef
	lastPlaceOrd Order
	// closeErrs[legIdx] 非空 → 该 leg CloseOrder 返回此 error（best-effort 场景用）。
	closeErrs map[int]error
	placeErrs map[int]error
}

func (e *fakeRollbackExecutor) Key() string { return e.venue }
func (e *fakeRollbackExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	idx := int(atomic.AddInt32(&e.placeCount, 1)) - 1
	e.lastPlaceOrd = o
	if err, ok := e.placeErrs[idx]; ok {
		return Fill{}, err
	}
	return Fill{OrderID: "rollback-place", Qty: o.Qty}, nil
}
func (e *fakeRollbackExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	idx := int(atomic.AddInt32(&e.closeCount, 1)) - 1
	e.lastCloseRef = ref
	if err, ok := e.closeErrs[idx]; ok {
		return Fill{}, err
	}
	return Fill{OrderID: "rollback-close", Qty: 1}, nil
}

// TestPersistOpenLegs_RollbackMT D-201 S2.1：2 腿 MT FILLED → rollbackFilledLegs 调 CloseOrder 2 次。
func TestPersistOpenLegs_RollbackMT(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts CASCADE`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	reg := NewRegistry()
	exe := &fakeRollbackExecutor{venue: "52993526:mt5"}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s}

	legs := []LegSpec{
		{VenueKey: "52993526:mt5", VenueSymbol: "AUDJPY", Side: "BUY", Qty: 0.01},
		{VenueKey: "52993526:mt5", VenueSymbol: "AUDJPY", Side: "SELL", Qty: 0.01},
	}
	results := []LegResult{
		{VenueKey: "52993526:mt5", OrderID: "mt-ticket-1", Status: "FILLED", Qty: 0.01, Price: 113.5},
		{VenueKey: "52993526:mt5", OrderID: "mt-ticket-2", Status: "FILLED", Qty: 0.01, Price: 113.5},
	}
	p.rollbackFilledLegs(ctx, 999, legs, results, 1)

	if exe.closeCount != 2 {
		t.Errorf("CloseOrder 调用次数=%d, want 2（2 腿 FILLED 都回滚）", exe.closeCount)
	}
	if exe.placeCount != 0 {
		t.Errorf("PlaceOrder 调用次数=%d, want 0（MT 腿用 CloseOrder）", exe.placeCount)
	}
	// 验证 CloseOrder 收到正确的 ticket
	if exe.lastCloseRef.OrderID != "mt-ticket-2" {
		t.Errorf("最后一次 CloseOrder OrderID=%q, want mt-ticket-2", exe.lastCloseRef.OrderID)
	}
}

// TestPersistOpenLegs_RollbackCEX D-201 S2.2：2 腿 CEX FILLED → rollbackFilledLegs 调 PlaceOrder 反向 2 次。
func TestPersistOpenLegs_RollbackCEX(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts CASCADE`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	reg := NewRegistry()
	exe := &fakeRollbackExecutor{venue: "okx:perp"}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s, csResolver: permissiveCSResolver{}} // D-311 R4：:perp 腿回滚换算需要 cs

	legs := []LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 0.001},
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 0.001},
	}
	results := []LegResult{
		{VenueKey: "okx:perp", OrderID: "cex-order-1", Status: "FILLED", Qty: 0.001, Price: 50000},
		{VenueKey: "okx:perp", OrderID: "cex-order-2", Status: "FILLED", Qty: 0.001, Price: 50000},
	}
	p.rollbackFilledLegs(ctx, 998, legs, results, 1)

	if exe.placeCount != 2 {
		t.Errorf("PlaceOrder 调用次数=%d, want 2（2 腿 CEX FILLED 反向回滚）", exe.placeCount)
	}
	if exe.closeCount != 0 {
		t.Errorf("CloseOrder 调用次数=%d, want 0（CEX 腿用 PlaceOrder 反向）", exe.closeCount)
	}
	// 验证 Side 取反：BUY→SELL，SELL→BUY
	// lastPlaceOrd 是第 2 腿（SELL → 反向 BUY）
	if exe.lastPlaceOrd.Side != "BUY" {
		t.Errorf("第 2 腿 SELL 反向应 BUY, got %s", exe.lastPlaceOrd.Side)
	}
}

// TestPersistOpenLegs_RollbackSkipUnknown D-201 S2.3：UNKNOWN 腿不回滚，只回滚 FILLED 腿。
func TestPersistOpenLegs_RollbackSkipUnknown(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts CASCADE`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	reg := NewRegistry()
	exe := &fakeRollbackExecutor{venue: "52993526:mt5"}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s}

	legs := []LegSpec{
		{VenueKey: "52993526:mt5", VenueSymbol: "AUDJPY", Side: "BUY", Qty: 0.01},
		{VenueKey: "52993526:mt5", VenueSymbol: "AUDJPY", Side: "SELL", Qty: 0.01},
	}
	results := []LegResult{
		{VenueKey: "52993526:mt5", OrderID: "mt-unknown-1", Status: "UNKNOWN", Qty: 0.01, Price: 113.5},
		{VenueKey: "52993526:mt5", OrderID: "mt-filled-2", Status: "FILLED", Qty: 0.01, Price: 113.5},
	}
	p.rollbackFilledLegs(ctx, 997, legs, results, 1)

	// UNKNOWN 腿不回滚，只回滚第 2 腿 FILLED
	if exe.closeCount != 1 {
		t.Errorf("CloseOrder 调用次数=%d, want 1（UNKNOWN 不回滚，只回滚 1 腿 FILLED）", exe.closeCount)
	}
	if exe.lastCloseRef.OrderID != "mt-filled-2" {
		t.Errorf("CloseOrder OrderID=%q, want mt-filled-2（只回滚 FILLED 腿）", exe.lastCloseRef.OrderID)
	}
}

// TestPersistOpenLegs_RollbackBestEffort D-201 S2.4：第 1 腿回滚失败 → 只 alert，不阻断第 2 腿回滚。
func TestPersistOpenLegs_RollbackBestEffort(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts CASCADE`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	reg := NewRegistry()
	exe := &fakeRollbackExecutor{
		venue:     "52993526:mt5",
		closeErrs: map[int]error{0: errors.New("broker timeout")}, // 第 1 腿 CloseOrder 失败
	}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s}

	legs := []LegSpec{
		{VenueKey: "52993526:mt5", VenueSymbol: "AUDJPY", Side: "BUY", Qty: 0.01},
		{VenueKey: "52993526:mt5", VenueSymbol: "AUDJPY", Side: "SELL", Qty: 0.01},
	}
	results := []LegResult{
		{VenueKey: "52993526:mt5", OrderID: "mt-ticket-1", Status: "FILLED", Qty: 0.01, Price: 113.5},
		{VenueKey: "52993526:mt5", OrderID: "mt-ticket-2", Status: "FILLED", Qty: 0.01, Price: 113.5},
	}
	p.rollbackFilledLegs(ctx, 996, legs, results, 1)

	// best-effort：第 1 腿失败不阻断第 2 腿 → CloseOrder 仍被调 2 次
	if exe.closeCount != 2 {
		t.Errorf("CloseOrder 调用次数=%d, want 2（best-effort：第 1 腿失败不阻断第 2 腿）", exe.closeCount)
	}
	// 验证第 1 腿回滚失败写 alert
	var alertCount int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE level='critical' AND message LIKE 'rollback failed%'`).Scan(&alertCount); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if alertCount != 1 {
		t.Errorf("rollback failed alert 行数=%d, want 1（第 1 腿回滚失败应写 critical alert）", alertCount)
	}
}

// TestPersistOpenLegs_RollbackAllLegs D-201 复审修正：persistOpenLegs 在第一个失败处 return，
// failedIdx 之后的腿已在 broker 端并发执行——回滚范围必须覆盖全部 results，不能只回滚 0~failedIdx。
// 场景：3 腿 FILLED，failedIdx=0（第 1 腿 persist 失败）→ 回滚全部 3 腿（不只第 1 腿）。
func TestPersistOpenLegs_RollbackAllLegs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE alerts CASCADE`); err != nil {
		t.Fatalf("truncate alerts: %v", err)
	}
	reg := NewRegistry()
	exe := &fakeRollbackExecutor{venue: "52993526:mt5"}
	reg.Register(exe)
	p := &Pipeline{registry: reg, store: s}

	legs := []LegSpec{
		{VenueKey: "52993526:mt5", VenueSymbol: "AUDJPY", Side: "BUY", Qty: 0.01},
		{VenueKey: "52993526:mt5", VenueSymbol: "AUDJPY", Side: "SELL", Qty: 0.01},
		{VenueKey: "52993526:mt5", VenueSymbol: "AUDJPY", Side: "BUY", Qty: 0.01},
	}
	results := []LegResult{
		{VenueKey: "52993526:mt5", OrderID: "mt-ticket-1", Status: "FILLED", Qty: 0.01, Price: 113.5},
		{VenueKey: "52993526:mt5", OrderID: "mt-ticket-2", Status: "FILLED", Qty: 0.01, Price: 113.5},
		{VenueKey: "52993526:mt5", OrderID: "mt-ticket-3", Status: "FILLED", Qty: 0.01, Price: 113.5},
	}
	// failedIdx=0：第 1 腿 persist 失败，但第 2/3 腿已在 broker 端 FILLED
	p.rollbackFilledLegs(ctx, 995, legs, results, 0)

	// 复审修正：回滚全部 3 腿（不只 0~failedIdx=1 腿）
	if exe.closeCount != 3 {
		t.Errorf("CloseOrder 调用次数=%d, want 3（failedIdx=0 但第 2/3 腿已 FILLED，回滚范围=全部 results）", exe.closeCount)
	}
}
