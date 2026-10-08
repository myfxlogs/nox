package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// D-195-C1：LiveGate 检查测试（DSN-gated 真库）。D-120：capital 面三条退役，
// 余 5 条（gate 记录/operator/env 一致/凭据/未过期）。

func testLiveGateStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	if _, err := s.Pool().Exec(ctx, `TRUNCATE live_gates CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// TestLiveGate_NoGateRecord §10.1.1：无 live_gate 记录 → Passed=false，reason 含"no active live gate record"。
func TestLiveGate_NoGateRecord(t *testing.T) {
	s := testLiveGateStore(t)
	lg := NewLiveGateChecker(s, nil)

	result := lg.Check(context.Background(), "", "", "")
	if result.Passed {
		t.Errorf("Passed=true, want false (no live gate record)")
	}
	found := false
	for _, r := range result.Reasons {
		if containsStr(r, "no active live gate record") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("reasons=%v, want one containing 'no active live gate record'", result.Reasons)
	}
	if result.HasGateRecord {
		t.Errorf("HasGateRecord=true, want false")
	}
}

// TestLiveGate_Expired §10.1.2：live_gate 过期 → Passed=false，reason 含"expired"。
func TestLiveGate_Expired(t *testing.T) {
	s := testLiveGateStore(t)
	ctx := context.Background()

	// 插入一条已过期的 live_gate
	past := time.Now().Add(-1 * time.Hour)
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO live_gates (scope_type, scope_key, account_key, strategy, operator,
			confirmed_at, expires_at, check_snapshot)
		VALUES ('global', '', '', '', 'operator-1', now(), $1, '{}'::jsonb)`, &past)
	if err != nil {
		t.Fatalf("insert expired live_gate: %v", err)
	}

	lg := NewLiveGateChecker(s, nil)
	result := lg.Check(ctx, "", "", "")
	if result.Passed {
		t.Errorf("Passed=true, want false (expired)")
	}
	if !result.HasGateRecord {
		t.Errorf("HasGateRecord=false, want true (record exists)")
	}
	if result.NotExpired {
		t.Errorf("NotExpired=true, want false")
	}
	found := false
	for _, r := range result.Reasons {
		if containsStr(r, "expired") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("reasons=%v, want one containing 'expired'", result.Reasons)
	}
}

// TestLiveGate_AllChecksPass：余下 5 条全通过 → Passed=true。
// 需 mock live_gate 行（未过期+operator 确认）；capital 面判据已随 D-120 退役。
func TestLiveGate_AllChecksPass(t *testing.T) {
	s := testLiveGateStore(t)
	ctx := context.Background()

	// live_gate（未过期，operator 确认）
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO live_gates (scope_type, scope_key, account_key, strategy, operator,
			confirmed_at, check_snapshot)
		VALUES ('global', '', '', '', 'operator-1', now(), '{}'::jsonb)`)
	if err != nil {
		t.Fatalf("insert live_gate: %v", err)
	}

	lg := NewLiveGateChecker(s, nil)
	result := lg.Check(ctx, "", "", "")
	if !result.Passed {
		t.Errorf("Passed=false, want true; reasons=%v", result.Reasons)
	}
}

// TestLiveGate_Step5AlwaysFails：步骤 5 无 live_gate 行 → Check 总是 Passed=false（fail-closed）。
func TestLiveGate_Step5AlwaysFails(t *testing.T) {
	s := testLiveGateStore(t)
	// 不插入任何 live_gate 行（模拟步骤 5 状态）
	lg := NewLiveGateChecker(s, nil)
	result := lg.Check(context.Background(), "", "", "")
	if result.Passed {
		t.Errorf("Passed=true, want false (step 5 no live_gate)")
	}
}

func containsStr(s, substr string) bool {
	return strings.Contains(s, substr)
}
