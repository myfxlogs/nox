// service_autotrade_state_test.go —— D-159 W6a S5：手动 RPC 不绕 CLOSE_ONLY 单测。
package dashboard

import (
	"context"
	"strings"
	"testing"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// setupAutotradeStateStore 构造真库 store + autotrade_state 种子（state 可空=TRUNCATE）。
func setupAutotradeStateStore(t *testing.T, state string) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if state != "" {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ($1, '', NULL)`, state); err != nil {
			t.Fatalf("insert autotrade_state: %v", err)
		}
	}
	return s
}

// TestCheckAutotradeState_CloseOnly D-159 W6a S5：CLOSE_ONLY → 返回 "CLOSE_ONLY"（调用方拒绝开仓）。
func TestCheckAutotradeState_CloseOnly(t *testing.T) {
	s := setupAutotradeStateStore(t, "CLOSE_ONLY")
	svc := &Service{store: s}
	state, err := svc.checkAutotradeState(context.Background())
	if err != nil {
		t.Fatalf("err = %v, want nil (CLOSE_ONLY is valid state)", err)
	}
	if state != "CLOSE_ONLY" {
		t.Errorf("state = %s, want CLOSE_ONLY", state)
	}
}

// TestCheckAutotradeState_Running D-159 W6a S5：RUNNING → 返回 "RUNNING"（放行）。
func TestCheckAutotradeState_Running(t *testing.T) {
	s := setupAutotradeStateStore(t, "RUNNING")
	svc := &Service{store: s}
	state, err := svc.checkAutotradeState(context.Background())
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if state != "RUNNING" {
		t.Errorf("state = %s, want RUNNING", state)
	}
}

// TestCheckAutotradeState_TableEmpty_FailClosed D-159 W6a S5：autotrade_state 表空 → fail-closed error。
func TestCheckAutotradeState_TableEmpty_FailClosed(t *testing.T) {
	s := setupAutotradeStateStore(t, "") // 不插入=表空
	svc := &Service{store: s}
	_, err := svc.checkAutotradeState(context.Background())
	if err == nil {
		t.Error("err = nil, want error (table empty → fail-closed)")
	}
	if !strings.Contains(err.Error(), "no rows") && !strings.Contains(err.Error(), "not found") {
		t.Errorf("err should contain 'no rows' or 'not found', got %v", err)
	}
}
