package pgstore

import (
	"context"
	"strings"
	"testing"
)

// TestMT4Retire_InsertRejects 退役锚：写边界 platform!=1 一律拒（纵深闸——
// RPC 层 D-470 已拒，store 层再堵直连写径），mt5=1 唯一合法。
func TestMT4Retire_InsertRejects(t *testing.T) {
	s := &Store{}
	if err := s.InsertBrokerAccount(context.Background(), &BrokerAccount{Venue: "x", Platform: 0}); err == nil || !strings.Contains(err.Error(), "已退役") {
		t.Fatalf("platform=0: err=%v, want 含「已退役」", err)
	}
	if err := s.InsertBrokerAccount(context.Background(), &BrokerAccount{Venue: "x", Platform: 2}); err == nil {
		t.Fatal("platform=2（未知值静默落 mt5）应拒")
	}
}
