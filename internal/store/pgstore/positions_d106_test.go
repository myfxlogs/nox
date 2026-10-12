package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestListPositions_ACTIVEFilter D-106：status="ACTIVE" → 4 活跃态任一（OPEN/OPENING/LEGGED/CLOSING）。
// 插入各状态持仓 → ACTIVE 只返回 4 活跃态，不含 CLOSED/FAILED。
func TestListPositions_ACTIVEFilter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 插入 6 种状态各 1 条。
	statuses := []string{"OPEN", "OPENING", "LEGGED", "CLOSING", "CLOSED", "FAILED"}
	for i, st := range statuses {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', $2, now())`,
			"D106TEST:SYM:vA:vB:"+st, st); err != nil {
			t.Fatalf("insert %s: %v", st, err)
		}
		_ = i
	}

	got, err := s.ListPositions(ctx, "ACTIVE", 50, 0)
	if err != nil {
		t.Fatalf("ListPositions ACTIVE: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("ACTIVE want 4 rows (OPEN/OPENING/LEGGED/CLOSING), got %d", len(got))
	}
	for _, p := range got {
		switch p.Status {
		case "OPEN", "OPENING", "LEGGED", "CLOSING":
			// ok
		default:
			t.Errorf("ACTIVE filter returned non-active status %q (id=%d)", p.Status, p.ID)
		}
	}
}

// TestCountPositions_ACTIVE D-106：CountPositions("ACTIVE") = 4 活跃态数。
func TestCountPositions_ACTIVE(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for _, st := range []string{"OPEN", "OPENING", "LEGGED", "CLOSING", "CLOSED", "FAILED"} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', $2, now())`,
			"D106CNT:SYM:vA:vB:"+st, st); err != nil {
			t.Fatalf("insert %s: %v", st, err)
		}
	}
	n, err := s.CountPositions(ctx, "ACTIVE")
	if err != nil {
		t.Fatalf("CountPositions ACTIVE: %v", err)
	}
	if n != 4 {
		t.Errorf("CountPositions ACTIVE want 4, got %d", n)
	}
}

// TestListPositions_OrderByClosedAtDesc D-106：ORDER BY COALESCE(closed_at, opened_at) DESC NULLS LAST。
// 3 CLOSED（closed_at 递增）+ 1 OPEN（closed_at NULL, opened_at=t2）。
// COALESCE(closed_at, opened_at)：CLOSED 行用 closed_at，OPEN 行用 opened_at。
// 排序键：CLOSED#3(t3) > OPEN(t2) > CLOSED#2(t2) > CLOSED#1(t1)。
// 同键（OPEN t2 vs CLOSED#2 t2）按 PostgreSQL 物理顺序（不硬断言同键顺序）。
// 断言：第 1 行 = CLOSED#3（closed_at=t3 最新）；最后 1 行 = CLOSED#1（closed_at=t1 最旧）。
func TestListPositions_OrderByClosedAtDesc(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t1 := time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	t2 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	t3 := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	// CLOSED#1: opened=t1, closed=t1（COALESCE=t1）
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at) VALUES ($1, 'SWAPARB', 'CLOSED', $2, $3)`,
		"D106ORD:SYM:vA:vB:C1", t1, t1); err != nil {
		t.Fatalf("insert C1: %v", err)
	}
	// CLOSED#2: opened=t1, closed=t2（COALESCE=t2）
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at) VALUES ($1, 'SWAPARB', 'CLOSED', $2, $3)`,
		"D106ORD:SYM:vA:vB:C2", t1, t2); err != nil {
		t.Fatalf("insert C2: %v", err)
	}
	// CLOSED#3: opened=t1, closed=t3（COALESCE=t3，最新）
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at) VALUES ($1, 'SWAPARB', 'CLOSED', $2, $3)`,
		"D106ORD:SYM:vA:vB:C3", t1, t3); err != nil {
		t.Fatalf("insert C3: %v", err)
	}
	// OPEN: opened=t2（COALESCE=t2，无 closed_at）
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'OPEN', $2)`,
		"D106ORD:SYM:vA:vB:OP", t2); err != nil {
		t.Fatalf("insert OP: %v", err)
	}
	got, err := s.ListPositions(ctx, "", 50, 0)
	if err != nil {
		t.Fatalf("ListPositions: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("want 4 rows, got %d", len(got))
	}
	// 第 1 行应为 CLOSED#3（COALESCE=t3 最新）。
	if got[0].OpportunityID != "D106ORD:SYM:vA:vB:C3" {
		t.Errorf("row[0] want C3 (closed_at=t3 最新), got %s", got[0].OpportunityID)
	}
	// 最后 1 行应为 CLOSED#1（COALESCE=t1 最旧）。
	if got[3].OpportunityID != "D106ORD:SYM:vA:vB:C1" {
		t.Errorf("row[3] want C1 (closed_at=t1 最旧), got %s", got[3].OpportunityID)
	}
}

// TestListPositions_Pagination D-106：limit/offset 分页边界。
func TestListPositions_Pagination(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	for i := 0; i < 5; i++ {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ($1, 'SWAPARB', 'CLOSED', now())`,
			"D106PG:SYM:vA:vB:"+string(rune('A'+i))); err != nil {
			t.Fatalf("insert %d: %v", i, err)
		}
	}
	// limit=2 offset=0 → 2 行
	page1, err := s.ListPositions(ctx, "CLOSED", 2, 0)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 2 {
		t.Errorf("page1 want 2 rows, got %d", len(page1))
	}
	// limit=2 offset=4 → 1 行（第 5 行）
	page3, err := s.ListPositions(ctx, "CLOSED", 2, 4)
	if err != nil {
		t.Fatalf("page3: %v", err)
	}
	if len(page3) != 1 {
		t.Errorf("page3 want 1 row, got %d", len(page3))
	}
	// limit=2 offset=6 → 0 行（越界）
	page4, err := s.ListPositions(ctx, "CLOSED", 2, 6)
	if err != nil {
		t.Fatalf("page4: %v", err)
	}
	if len(page4) != 0 {
		t.Errorf("page4 want 0 rows, got %d", len(page4))
	}
}
