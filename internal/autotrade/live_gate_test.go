package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// D-195-C1：LiveGate 8 条检查测试（DSN-gated 真库）。

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
	if _, err := s.Pool().Exec(ctx, `TRUNCATE live_gates, hypotheses, capital_snapshots CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

// TestLiveGate_NoGateRecord §10.1.1：无 live_gate 记录 → Passed=false，reason 含"no active live gate record"。
func TestLiveGate_NoGateRecord(t *testing.T) {
	s := testLiveGateStore(t)
	lg := NewLiveGateChecker(s, 5*time.Minute, nil)

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

	lg := NewLiveGateChecker(s, 5*time.Minute, nil)
	result := lg.Check(ctx, "", "", "")
	if result.Passed {
		t.Errorf("Passed=true, want false (expired)")
	}
	if result.HasGateRecord != true {
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

// TestLiveGate_DrawdownThreshold §10.1.15：回撤 >= 5% → Passed=false，reason 含"drawdown"。
func TestLiveGate_DrawdownThreshold(t *testing.T) {
	s := testLiveGateStore(t)
	ctx := context.Background()

	// 插入 live_gate（未过期）
	_, err := s.Pool().Exec(ctx, `
		INSERT INTO live_gates (scope_type, scope_key, account_key, strategy, operator,
			confirmed_at, check_snapshot)
		VALUES ('global', '', '', '', 'operator-1', now(), '{}'::jsonb)`)
	if err != nil {
		t.Fatalf("insert live_gate: %v", err)
	}

	// 插入 approved hypothesis
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO hypotheses (hypothesis_id, status, net_return_hurdle, max_drawdown, principal_rule, kill_rule, approved_at)
		VALUES ('HYP-TEST-1', 'approved', 8.0, 10.0, 'rule', 'kill', now())`)
	if err != nil {
		t.Fatalf("insert hypothesis: %v", err)
	}

	// 插入 capital snapshots 制造 6% 回撤：HWM=100, current=94
	future := time.Now().Add(1 * time.Hour) // source_ts 在未来确保被选中
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO capital_snapshots (scope_type, scope_key, source, source_ts, received_ts,
			quality, liquidatable_equity_usd, created_at)
		VALUES
		('overall', '', 'manual', $1, now(), 'actual', 100, now()),
		('overall', '', 'manual', $1, now(), 'actual', 94, now())`, &future)
	if err != nil {
		t.Fatalf("insert snapshots: %v", err)
	}

	lg := NewLiveGateChecker(s, 5*time.Minute, nil)
	result := lg.Check(ctx, "", "", "")
	if result.Passed {
		t.Errorf("Passed=true, want false (drawdown >= 5 pct)")
	}
	if result.DrawdownOK {
		t.Errorf("DrawdownOK=true, want false")
	}
	found := false
	for _, r := range result.Reasons {
		if containsStr(r, "drawdown") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("reasons=%v, want one containing 'drawdown'", result.Reasons)
	}
}

// TestLiveGate_AllChecksPass：8 条全通过 → Passed=true。
// 需 mock live_gate 行 + approved hypothesis + fresh snapshot + drawdown < 5%。
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

	// approved hypothesis
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO hypotheses (hypothesis_id, status, net_return_hurdle, max_drawdown, principal_rule, kill_rule, approved_at)
		VALUES ('HYP-TEST-2', 'approved', 8.0, 10.0, 'rule', 'kill', now())`)
	if err != nil {
		t.Fatalf("insert hypothesis: %v", err)
	}

	// fresh capital snapshot（account scope, scope_key='' 匹配 LiveGate 第 5 条查询）
	// HWM=100, current=100 → 0% 回撤
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO capital_snapshots (scope_type, scope_key, source, source_ts, received_ts,
			quality, liquidatable_equity_usd, created_at)
		VALUES
		('account', '', 'manual', now(), now(), 'actual', 100, now()),
		('overall', '', 'manual', now(), now(), 'actual', 100, now())`)
	if err != nil {
		t.Fatalf("insert snapshots: %v", err)
	}

	lg := NewLiveGateChecker(s, 5*time.Minute, nil)
	result := lg.Check(ctx, "", "", "")
	if !result.Passed {
		t.Errorf("Passed=false, want true; reasons=%v", result.Reasons)
	}
}

// TestLiveGate_Step5AlwaysFails：步骤 5 无 live_gate 行 → Check 总是 Passed=false（fail-closed）。
func TestLiveGate_Step5AlwaysFails(t *testing.T) {
	s := testLiveGateStore(t)
	// 不插入任何 live_gate 行（模拟步骤 5 状态）
	lg := NewLiveGateChecker(s, 5*time.Minute, nil)
	result := lg.Check(context.Background(), "", "", "")
	if result.Passed {
		t.Errorf("Passed=true, want false (step 5 no live_gate)")
	}
}

// containsStr 简单字符串包含检查（避免 import strings）。
func containsStr(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStrHelper(s, substr))
}

func containsStrHelper(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
