package pgstore

import (
	"context"
	"testing"
)

// insertPosition 直接插入一条 positions 记录（测试种子）。
func insertPosition(t *testing.T, s *Store, oppID, status string) {
	t.Helper()
	_, err := s.pool.Exec(context.Background(),
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ($1, $2, $3, 0)`,
		oppID, "CROSS_EXCHANGE", status)
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
