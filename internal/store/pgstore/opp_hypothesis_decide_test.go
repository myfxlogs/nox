package pgstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// setupDecideStore D-623：裁决写口测试基座——proposed 假设行落种。
func setupDecideStore(t *testing.T, status string) (*Store, int64) {
	t.Helper()
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_opp_hypothesis, alerts`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	id, _, _, err := s.UpsertHypothesis(ctx, OppHypothesis{
		Venue: "vX", CategoryKey: "vx|opt|usd", InstType: "opt", Shape: "usd", Sample: "S1", Detail: "d",
	})
	if err != nil {
		t.Fatalf("seed hypothesis: %v", err)
	}
	if status != "proposed" {
		if _, err := s.Pool().Exec(ctx, `UPDATE fin_opp_hypothesis SET status=$1 WHERE id=$2`, status, id); err != nil {
			t.Fatalf("set status %s: %v", status, err)
		}
	}
	return s, id
}

// TestDecideHypothesis_Approved D-623：proposed→approved 含 decided_by/decided_at+alert 留痕。
func TestDecideHypothesis_Approved(t *testing.T) {
	s, id := setupDecideStore(t, "proposed")
	ctx := context.Background()
	if err := s.DecideHypothesis(ctx, id, "approved", "决策席", "契合 generic_yield", "D-623"); err != nil {
		t.Fatalf("decide approved: %v", err)
	}
	var st, by, rr string
	var at *time.Time
	if err := s.Pool().QueryRow(ctx,
		`SELECT status, decided_by, COALESCE(reject_reason,''), decided_at FROM fin_opp_hypothesis WHERE id=$1`, id).
		Scan(&st, &by, &rr, &at); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if st != "approved" || by != "决策席" || at == nil || rr != "" {
		t.Fatalf("approved 落账错: status=%s by=%s at=%v rr=%s", st, by, at, rr)
	}
	var msg string
	if err := s.Pool().QueryRow(ctx,
		`SELECT message FROM alerts WHERE level='info' AND message LIKE '假设%' ORDER BY id DESC LIMIT 1`).Scan(&msg); err != nil {
		t.Fatalf("alert 留痕缺: %v", err)
	}
	if !strings.Contains(msg, "决策席") || !strings.Contains(msg, "D-623") {
		t.Fatalf("alert 文案缺 actor/证据: %s", msg)
	}
}

// TestDecideHypothesis_Rejected D-623：proposed→rejected 含 reject_reason。
func TestDecideHypothesis_Rejected(t *testing.T) {
	s, id := setupDecideStore(t, "proposed")
	ctx := context.Background()
	if err := s.DecideHypothesis(ctx, id, "rejected", "决策席", "息面不达标", "D-623"); err != nil {
		t.Fatalf("decide rejected: %v", err)
	}
	var st, rr string
	if err := s.Pool().QueryRow(ctx,
		`SELECT status, COALESCE(reject_reason,'') FROM fin_opp_hypothesis WHERE id=$1`, id).Scan(&st, &rr); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if st != "rejected" || rr != "息面不达标" {
		t.Fatalf("rejected 落账错: status=%s rr=%s", st, rr)
	}
}

// TestDecideHypothesis_Refusals D-623：非 proposed/不存在 id 拒受理。
func TestDecideHypothesis_Refusals(t *testing.T) {
	s, id := setupDecideStore(t, "approved")
	ctx := context.Background()
	if err := s.DecideHypothesis(ctx, id, "rejected", "x", "r", "e"); err == nil || !strings.Contains(err.Error(), "非 proposed") {
		t.Fatalf("approved 行重判应拒: %v", err)
	}
	if err := s.DecideHypothesis(ctx, 999999, "approved", "x", "r", "e"); err == nil || !strings.Contains(err.Error(), "不存在") {
		t.Fatalf("不存在 id 应拒: %v", err)
	}
}
