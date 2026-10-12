package pgstore

import (
	"context"
	"strings"
	"testing"

	"arb/internal/testutil"
)

// TestInsertCEXPrivateEvent_D450：审计表落行+字段回读（0136 migration 生效）。
func TestInsertCEXPrivateEvent_D450(t *testing.T) {
	s, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `DELETE FROM cex_private_events`); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertCEXPrivateEvent(ctx, "gate", "perp", "futures.orders", "777", []byte(`[{"id":777}]`)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	if err := s.InsertCEXPrivateEvent(ctx, "binance", "perp", "orders", "", []byte(`{"i":888}`)); err != nil {
		t.Fatalf("insert2: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM cex_private_events WHERE venue='gate' AND channel='futures.orders' AND event_key='777'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("gate row n=%d", n)
	}
	// JSONB 原文回读。
	var payload string
	if err := s.pool.QueryRow(ctx, `SELECT payload::text FROM cex_private_events WHERE venue='binance'`).Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(payload, `"i": 888`) {
		t.Fatalf("payload=%s", payload)
	}
}
