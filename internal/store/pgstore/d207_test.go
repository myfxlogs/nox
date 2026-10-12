package pgstore

// d207_test.go —— D-207③ OutstandingDCIQty 窗按 response.settleDate 截（真库锚）。

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestD207_OutstandingSettleDateWindow(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	s.pool.Exec(ctx, `DELETE FROM fin_dci_order WHERE request_id LIKE 'd207test%'`)
	past := time.Now().Add(-time.Hour).UnixMilli()
	future := time.Now().Add(24 * time.Hour).UnixMilli()
	// 旧 created 行（created+tenor 早已过期）但 settleDate 在未来 → 计入（回执原值锚定）。
	old := &DCIOrder{RequestID: "d207test|future", Venue: "binance", ProductRef: "PX",
		Underlying: "BTC", OptType: "PUT", InvestCcy: "USDT", Qty: 100}
	oid, _, err := s.CreateDCIOrder(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishDCIOrder(ctx, oid, "filled",
		fmt.Sprintf(`{"settleDate":%d}`, future)); err != nil {
		t.Fatal(err)
	}
	s.pool.Exec(ctx, `UPDATE fin_dci_order SET created_at=now()-interval '30 days' WHERE id=$1`, oid)
	// settleDate 已过 → 不计入（+4h 过冲实证修复：即使 created+tenor 仍未来也截）。
	exp := &DCIOrder{RequestID: "d207test|past", Venue: "binance", ProductRef: "PY",
		Underlying: "BTC", OptType: "PUT", InvestCcy: "USDT", Qty: 500}
	eid, _, err := s.CreateDCIOrder(ctx, exp)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.FinishDCIOrder(ctx, eid, "filled",
		fmt.Sprintf(`{"settleDate":%d}`, past)); err != nil {
		t.Fatal(err)
	}
	got, err := s.OutstandingDCIQty(ctx, "binance", "USDT")
	if err != nil {
		t.Fatal(err)
	}
	if got != 100 {
		t.Fatalf("settleDate 窗应只计 100（past 截除）, got %v", got)
	}
	s.pool.Exec(ctx, `DELETE FROM fin_dci_order WHERE request_id LIKE 'd207test%'`)
}
