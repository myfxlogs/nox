package pgstore

import (
	"context"
	"testing"
)

// D-195-B：position_legs attempt 字段 + Position intent 字段 CRUD 测试（DSN-gated 真库）。

func testAttemptStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// TestPositionLegs_AttemptFields：CreatePositionLeg 写 attempt_no + client_order_id + filled_qty，回读一致。
func TestPositionLegs_AttemptFields(t *testing.T) {
	s := testAttemptStore(t)
	ctx := context.Background()

	posID, err := s.CreatePosition(ctx, &Position{Type: "MEANREVERT", Status: "OPENING", RequestID: "req-1"})
	if err != nil {
		t.Fatalf("CreatePosition: %v", err)
	}

	qty := 1.5
	price := 113.5
	if err := s.CreatePositionLeg(ctx, &PositionLeg{
		PositionID:    posID,
		LegNo:         0,
		Phase:         "OPEN",
		AttemptNo:     1,
		Venue:         "vA:mt5",
		Symbol:        "AUDJPY",
		Side:          "BUY",
		Qty:           1.5,
		PriceExpected: 113.0,
		PriceActual:   &price,
		Status:        "FILLED",
		ClientOrderID: "req-1-1-0-1",
		RequestedQty:  &qty,
		FilledQty:     &qty,
		AvgFillPrice:  &price,
	}); err != nil {
		t.Fatalf("CreatePositionLeg: %v", err)
	}

	legs, err := s.ListPositionLegs(ctx, posID)
	if err != nil {
		t.Fatalf("ListPositionLegs: %v", err)
	}
	if len(legs) != 1 {
		t.Fatalf("legs=%d, want 1", len(legs))
	}
	l := legs[0]
	if l.AttemptNo != 1 {
		t.Errorf("AttemptNo=%d, want 1", l.AttemptNo)
	}
	if l.ClientOrderID != "req-1-1-0-1" {
		t.Errorf("ClientOrderID=%s, want req-1-1-0-1", l.ClientOrderID)
	}
	if l.FilledQty == nil || *l.FilledQty != 1.5 {
		t.Errorf("FilledQty=%v, want 1.5", l.FilledQty)
	}
}

// TestPositionLegs_AttemptNoConflict：同 (position_id, leg_no, phase) 不同 attempt_no 都写入成功；
// 同 attempt_no 重复写 → ErrTerminalState。
func TestPositionLegs_AttemptNoConflict(t *testing.T) {
	s := testAttemptStore(t)
	ctx := context.Background()

	posID, _ := s.CreatePosition(ctx, &Position{Type: "MEANREVERT", Status: "OPENING"})

	// attempt_no=1 写入成功
	if err := s.CreatePositionLeg(ctx, &PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", AttemptNo: 1, Venue: "vA:mt5",
		Side: "BUY", Qty: 1.0, PriceExpected: 100.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("CreatePositionLeg attempt=1: %v", err)
	}
	// attempt_no=2 写入成功（同 leg 不同 attempt）
	if err := s.CreatePositionLeg(ctx, &PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", AttemptNo: 2, Venue: "vA:mt5",
		Side: "BUY", Qty: 1.0, PriceExpected: 100.0, Status: "FILLED",
	}); err != nil {
		t.Fatalf("CreatePositionLeg attempt=2: %v", err)
	}
	// 同 attempt_no=1 重复写 → ErrTerminalState
	err := s.CreatePositionLeg(ctx, &PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", AttemptNo: 1, Venue: "vA:mt5",
		Side: "BUY", Qty: 1.0, PriceExpected: 100.0, Status: "FILLED",
	})
	if err != ErrTerminalState {
		t.Errorf("重复 attempt=1: err=%v, want ErrTerminalState", err)
	}
}

// TestPositionLegs_ClientOrderIDUnique：同 (venue, account, client_order_id) 二次插入冲突。
func TestPositionLegs_ClientOrderIDUnique(t *testing.T) {
	s := testAttemptStore(t)
	ctx := context.Background()

	posID1, _ := s.CreatePosition(ctx, &Position{Type: "MEANREVERT", Status: "OPENING"})
	posID2, _ := s.CreatePosition(ctx, &Position{Type: "MEANREVERT", Status: "OPENING"})

	// 第一个 leg 写 client_order_id
	if err := s.CreatePositionLeg(ctx, &PositionLeg{
		PositionID: posID1, LegNo: 0, Phase: "OPEN", AttemptNo: 1,
		Venue: "vA:mt5", Account: "acc1", Side: "BUY", Qty: 1.0, PriceExpected: 100.0,
		Status: "FILLED", ClientOrderID: "coid-unique-1",
	}); err != nil {
		t.Fatalf("CreatePositionLeg 1: %v", err)
	}
	// 第二个 leg 同 (venue, account, client_order_id) → 冲突
	err := s.CreatePositionLeg(ctx, &PositionLeg{
		PositionID: posID2, LegNo: 0, Phase: "OPEN", AttemptNo: 1,
		Venue: "vA:mt5", Account: "acc1", Side: "BUY", Qty: 1.0, PriceExpected: 100.0,
		Status: "FILLED", ClientOrderID: "coid-unique-1",
	})
	if err == nil {
		t.Error("重复 client_order_id: err=nil, want conflict error")
	}
}

// TestPosition_RequestIdIdempotency：GetPositionByRequestID 写入 → 查询返回；无匹配 → nil, nil。
func TestPosition_RequestIdIdempotency(t *testing.T) {
	s := testAttemptStore(t)
	ctx := context.Background()

	posID, err := s.CreatePosition(ctx, &Position{Type: "MEANREVERT", Status: "OPENING", RequestID: "req-idem-1"})
	if err != nil {
		t.Fatalf("CreatePosition: %v", err)
	}

	// 查询存在
	p, err := s.GetPositionByRequestID(ctx, "req-idem-1")
	if err != nil {
		t.Fatalf("GetPositionByRequestID: %v", err)
	}
	if p == nil {
		t.Fatal("p=nil, want non-nil")
	}
	if p.ID != posID {
		t.Errorf("ID=%d, want %d", p.ID, posID)
	}
	if p.RequestID != "req-idem-1" {
		t.Errorf("RequestID=%s, want req-idem-1", p.RequestID)
	}

	// 无匹配 → nil, nil
	p2, err := s.GetPositionByRequestID(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetPositionByRequestID nonexistent: %v", err)
	}
	if p2 != nil {
		t.Errorf("p2=%+v, want nil", p2)
	}
}

// TestPosition_CreateWithIntentFields：CreatePosition 写 request_id/dedup_key/environment，回读一致；
// 空字符串 → NULL（不触发唯一约束）。
func TestPosition_CreateWithIntentFields(t *testing.T) {
	s := testAttemptStore(t)
	ctx := context.Background()

	// 写入完整 intent 字段
	posID, err := s.CreatePosition(ctx, &Position{
		Type:        "MEANREVERT",
		Status:      "OPENING",
		RequestID:   "req-full-1",
		DedupKey:    "dedup-1",
		Environment: "demo",
	})
	if err != nil {
		t.Fatalf("CreatePosition: %v", err)
	}
	p, _ := s.GetPositionByRequestID(ctx, "req-full-1")
	if p == nil {
		t.Fatal("p=nil")
	}
	if p.ID != posID {
		t.Errorf("ID=%d, want %d", p.ID, posID)
	}
	if p.DedupKey != "dedup-1" {
		t.Errorf("DedupKey=%s, want dedup-1", p.DedupKey)
	}
	if p.Environment != "demo" {
		t.Errorf("Environment=%s, want demo", p.Environment)
	}

	// 空字符串 → NULL（两个空 request_id position 不冲突）
	_, err = s.CreatePosition(ctx, &Position{Type: "MEANREVERT", Status: "OPENING"})
	if err != nil {
		t.Fatalf("CreatePosition empty intent: %v", err)
	}
	_, err = s.CreatePosition(ctx, &Position{Type: "MEANREVERT", Status: "OPENING"})
	if err != nil {
		t.Fatalf("CreatePosition empty intent 2: %v (空字符串→NULL 不应触发唯一约束)", err)
	}
}
