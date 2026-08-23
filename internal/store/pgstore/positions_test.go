package pgstore

import (
	"context"
	"errors"
	"testing"
)

// insertPosition 直接插入一条 positions 记录（测试种子）。
func insertPosition(t *testing.T, s *Store, oppID, status string) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ($1, $2, $3, 0)`,
		oppID, "CROSS_VENUE", status)
	if err != nil {
		t.Fatalf("insert position %s/%s: %v", oppID, status, err)
	}
}

// TestCountOpenPositions_Statuses 验证风控计数口径（D-058）：只计 OPEN+OPENING+LEGGED，
// CLOSED/FAILED 不计。
func TestCountOpenPositions_Statuses(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate positions: %v", err)
	}
	for _, st := range []string{"OPEN", "OPENING", "LEGGED", "CLOSED", "FAILED"} {
		insertPosition(t, s, "opp-"+st, st)
	}
	n, err := s.CountOpenPositions(ctx)
	if err != nil {
		t.Fatalf("CountOpenPositions: %v", err)
	}
	if n != 3 {
		t.Errorf("want 3 (OPEN+OPENING+LEGGED), got %d", n)
	}
}

// TestHasOpenPositionByOpportunity_States 验证幂等防重语义（D-058）：
// OPEN/OPENING/LEGGED 任一存在 → true（重复提交拒绝）；CLOSED/FAILED → false（可重入）；
// 无记录 → false。
func TestHasOpenPositionByOpportunity_States(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate positions: %v", err)
	}
	ok, err := s.HasOpenPositionByOpportunity(ctx, "opp-absent")
	if err != nil {
		t.Fatalf("HasOpenPositionByOpportunity(absent): %v", err)
	}
	if ok {
		t.Errorf("absent want false, got true")
	}
	for _, st := range []string{"OPEN", "OPENING", "LEGGED"} {
		insertPosition(t, s, "opp-"+st, st)
		got, err := s.HasOpenPositionByOpportunity(ctx, "opp-"+st)
		if err != nil {
			t.Fatalf("HasOpenPositionByOpportunity(%s): %v", st, err)
		}
		if !got {
			t.Errorf("%s want true (block re-execute), got false", st)
		}
	}
	for _, st := range []string{"CLOSED", "FAILED"} {
		insertPosition(t, s, "opp-"+st, st)
		got, err := s.HasOpenPositionByOpportunity(ctx, "opp-"+st)
		if err != nil {
			t.Fatalf("HasOpenPositionByOpportunity(%s): %v", st, err)
		}
		if got {
			t.Errorf("%s want false (re-entry allowed), got true", st)
		}
	}
}

// TestUpdatePositionStatus_TerminalGuard 终态不可逆守卫（D-077 修复⑩ G4）。
// CLOSED/FAILED 不可回退 → ErrTerminalState；合法转换（OPENING→OPEN、LEGGED→OPEN）正常。
func TestUpdatePositionStatus_TerminalGuard(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate positions: %v", err)
	}
	// 合法转换：OPENING → OPEN
	insertPosition(t, s, "opp-open", "OPENING")
	var openID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id='opp-open'`).Scan(&openID); err != nil {
		t.Fatalf("query open id: %v", err)
	}
	if err := s.UpdatePositionStatus(ctx, openID, "OPEN"); err != nil {
		t.Fatalf("OPENING→OPEN want nil, got %v", err)
	}
	// 合法转换：LEGGED → OPEN
	insertPosition(t, s, "opp-legged", "LEGGED")
	var leggedID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id='opp-legged'`).Scan(&leggedID); err != nil {
		t.Fatalf("query legged id: %v", err)
	}
	if err := s.UpdatePositionStatus(ctx, leggedID, "OPEN"); err != nil {
		t.Fatalf("LEGGED→OPEN want nil, got %v", err)
	}
	// 终态不可回退：CLOSED → OPEN
	insertPosition(t, s, "opp-closed", "CLOSED")
	var closedID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id='opp-closed'`).Scan(&closedID); err != nil {
		t.Fatalf("query closed id: %v", err)
	}
	if err := s.UpdatePositionStatus(ctx, closedID, "OPEN"); !errors.Is(err, ErrTerminalState) {
		t.Errorf("CLOSED→OPEN want ErrTerminalState, got %v", err)
	}
	// 终态不可回退：FAILED → OPEN
	insertPosition(t, s, "opp-failed", "FAILED")
	var failedID int64
	if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id='opp-failed'`).Scan(&failedID); err != nil {
		t.Fatalf("query failed id: %v", err)
	}
	if err := s.UpdatePositionStatus(ctx, failedID, "OPEN"); !errors.Is(err, ErrTerminalState) {
		t.Errorf("FAILED→OPEN want ErrTerminalState, got %v", err)
	}
}

// TestUpdatePositionMeta_Typed D-087 §3.4：typed 化后各型字段语义分离。
// FUNDING_HEDGE → NetBps=年化百分点 + AnnualPct=同值 / SWAPARB → EntrySpreadBps + SwapBpsDaily + AnnualPct=swap×365/100 / CROSS_VENUE → NetBps only。
func TestUpdatePositionMeta_Typed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate positions: %v", err)
	}
	tests := []struct {
		name    string
		posType string
		meta    PositionMeta
		wantPct *float64 // nil = 期望 NULL
	}{
		{
			"FUNDING_HEDGE 直存年化",
			"FUNDING_HEDGE",
			PositionMeta{NetBps: floatPtr(10.95), AnnualPct: floatPtr(10.95)},
			floatPtr(10.95),
		},
		{
			"SWAPARB typed 双字段",
			"SWAPARB",
			PositionMeta{EntrySpreadBps: floatPtr(-1.0), SwapBpsDaily: floatPtr(0.5), AnnualPct: floatPtr(0.5 * 365 / 100)},
			floatPtr(0.5 * 365 / 100),
		},
		{
			"CROSS_VENUE 收敛型 NULL annualPct",
			"CROSS_VENUE",
			PositionMeta{NetBps: floatPtr(15.0)},
			nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			oppID := "test-" + tt.posType
			if _, err := s.pool.Exec(ctx,
				`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ($1, $2, 'OPEN', 0)`,
				oppID, tt.posType); err != nil {
				t.Fatalf("insert: %v", err)
			}
			var id int64
			if err := s.pool.QueryRow(ctx, `SELECT id FROM positions WHERE opportunity_id=$1`, oppID).Scan(&id); err != nil {
				t.Fatalf("query id: %v", err)
			}
			if err := s.UpdatePositionMeta(ctx, id, tt.posType, tt.meta); err != nil {
				t.Fatalf("UpdatePositionMeta: %v", err)
			}
			var gotPct *float64
			if err := s.pool.QueryRow(ctx, `SELECT expected_annual_pct FROM positions WHERE id=$1`, id).Scan(&gotPct); err != nil {
				t.Fatalf("select expected_annual_pct: %v", err)
			}
			if tt.wantPct == nil {
				if gotPct != nil {
					t.Errorf("want NULL, got %v", *gotPct)
				}
			} else {
				if gotPct == nil {
					t.Errorf("want %v, got NULL", *tt.wantPct)
				} else if abs(*gotPct-*tt.wantPct) > 0.0001 {
					t.Errorf("want %v, got %v", *tt.wantPct, *gotPct)
				}
			}
		})
	}
}

func floatPtr(v float64) *float64 { return &v }

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// TestListPositions_NullExpectedNetBps D-087 返修④ 防回归：
// D-087 挪列后 SWAPARB 行 expected_net_bps 合法 NULL，Position.ExpectedNetBps 非指针 float64
// → 4 条 SELECT 直扫 NULL 进非指针 dest 报 Scan error（ListPositions 500 + monitor/autotrade 致盲）。
// 修法：4 处 SELECT 加 COALESCE(expected_net_bps, 0)。本测试插 NULL 行走全链 4 个方法 + settlement load。
// DSN-gated：无 ARB_TEST_PG_DSN 跳过（go test ./... 无 PG 环境仍全过）。
func TestListPositions_NullExpectedNetBps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions CASCADE`); err != nil {
		t.Fatalf("truncate positions: %v", err)
	}

	// 插 3 行：SWAPARB NULL（D-087 挪列合法空态）/ CROSS_VENUE 非零 / SWAPARB OPEN NULL。
	// 用裸 SQL 绕过 insertPosition helper（它写 0 不写 NULL），直造 NULL 场景。
	rows := []struct {
		oppID, posType, status string
		netBps                 interface{} // nil → NULL
	}{
		{"opp-swap-closed", "SWAPARB", "CLOSED", nil},
		{"opp-cross-open", "CROSS_VENUE", "OPEN", 15.0},
		{"opp-swap-open", "SWAPARB", "OPEN", nil},
	}
	var swapOpenID int64
	for _, r := range rows {
		var id int64
		err := s.pool.QueryRow(ctx, `
			INSERT INTO positions (opportunity_id, type, status, expected_net_bps)
			VALUES ($1, $2, $3, $4) RETURNING id
		`, r.oppID, r.posType, r.status, r.netBps).Scan(&id)
		if err != nil {
			t.Fatalf("insert %s: %v", r.oppID, err)
		}
		if r.oppID == "opp-swap-open" {
			swapOpenID = id
		}
	}

	// 1. ListPositions（含 NULL 行，COALESCE 前→Scan error 500；后→0）
	got, err := s.ListPositions(ctx, "", 50, 0)
	if err != nil {
		t.Fatalf("ListPositions with NULL expected_net_bps: %v（COALESCE 未生效？）", err)
	}
	if len(got) != 3 {
		t.Errorf("ListPositions 返回 %d 行, want 3", len(got))
	}
	// NULL 行 ExpectedNetBps 应=0（COALESCE 语义），非 NaN/崩溃
	for _, p := range got {
		if p.ExpectedNetBps != 0 && p.Type == "SWAPARB" {
			t.Errorf("SWAPARB 行 ExpectedNetBps=%.2f, want 0（NULL→COALESCE→0）", p.ExpectedNetBps)
		}
	}

	// 2. ListActivePositions（2 个 OPEN 行：CROSS_VENUE + SWAPARB，均含 NULL 安全）
	active, err := s.ListActivePositions(ctx)
	if err != nil {
		t.Fatalf("ListActivePositions with NULL: %v", err)
	}
	if len(active) != 2 {
		t.Errorf("ListActivePositions 返回 %d 行, want 2（CROSS_VENUE OPEN + SWAPARB OPEN）", len(active))
	}

	// 3. ListOpenPositionsByType（SWAPARB OPEN NULL）
	byType, err := s.ListOpenPositionsByType(ctx, []string{"SWAPARB"})
	if err != nil {
		t.Fatalf("ListOpenPositionsByType with NULL: %v", err)
	}
	if len(byType) != 1 {
		t.Errorf("ListOpenPositionsByType 返回 %d 行, want 1", len(byType))
	}

	// 4. GetPosition（settlement load 路径，NULL 行）
	p, err := s.GetPosition(ctx, swapOpenID)
	if err != nil {
		t.Fatalf("GetPosition with NULL expected_net_bps: %v（settlement load 路径 COALESCE 未生效？）", err)
	}
	if p == nil {
		t.Fatalf("GetPosition 返回 nil，期望行存在")
	}
	if p.ExpectedNetBps != 0 {
		t.Errorf("GetPosition SWAPARB NULL 行 ExpectedNetBps=%.2f, want 0", p.ExpectedNetBps)
	}
	if p.Type != "SWAPARB" {
		t.Errorf("GetType=%q, want SWAPARB", p.Type)
	}
}
