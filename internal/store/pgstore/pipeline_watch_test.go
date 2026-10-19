package pgstore

// PipelineFunnelBacklog 三段探针锚（§6.1③ 系统自盯版）：
// 只计等推进态（proposed/proposed/pending），decided 行（approved/rejected/
// unqualified/expired）不入计；零行段 Count=0+Oldest=nil。

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestPipelineFunnelBacklog(t *testing.T) {
	st := sharedTestStore(t)
	prepareTestSchema(t, st)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `DELETE FROM carry_graduations; DELETE FROM paper_spot_carry; DELETE FROM fin_opp_hypothesis; DELETE FROM fin_proposal`); err != nil {
		t.Fatalf("clear tables: %v", err)
	}
	// paper_id FK 父行（carry_graduations_paper_id_fkey → paper_spot_carry）。
	// 同符号 active 仓有部分唯一索引——每父行独立符号。
	for i := 0; i < 4; i++ {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO paper_spot_carry (id, symbol, side, entry_funding_ann, qty_usd, venue_a, venue_b) VALUES ($1,$2,'long',0,100,'binance','okx')`,
			888000+i, fmt.Sprintf("PW%02dUSDT", i)); err != nil {
			t.Fatalf("seed paper parent %d: %v", i, err)
		}
	}
	old := time.Now().Add(-20 * 24 * time.Hour)
	fresh := time.Now().Add(-1 * time.Hour)
	// 假设段：2 proposed（一老一鲜）+ approved/rejected 不入计。
	for i, s := range []struct{ key, status string }{
		{"PW_H_OLD", "proposed"}, {"PW_H_NEW", "proposed"}, {"PW_H_APP", "approved"}, {"PW_H_REJ", "rejected"},
	} {
		ts := old
		if i == 1 {
			ts = fresh
		}
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO fin_opp_hypothesis (venue, category_key, status, first_seen) VALUES ('gate',$1,$2,$3)`,
			s.key, s.status, ts); err != nil {
			t.Fatalf("seed hypothesis %s: %v", s.key, err)
		}
	}
	// 提案段：1 proposed + expired 不入计。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_proposal (template_id, exec_track, status, created_at) VALUES ('pw_t','test','proposed',$1), ('pw_t','test','expired',$2)`,
		old, fresh); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	// 毕业段：3 pending（最老=old）+ unqualified 不入计。
	for i := 0; i < 3; i++ {
		ts := fresh
		if i == 0 {
			ts = old
		}
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO carry_graduations (paper_id, symbol, structure, age_days, accrued_usd, status, created_at, venue_a, venue_b) VALUES (888000+$1,'PWUSDT','test',0,0,'pending',$2,'binance','okx')`,
			i, ts); err != nil {
			t.Fatalf("seed graduation %d: %v", i, err)
		}
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO carry_graduations (paper_id, symbol, structure, age_days, accrued_usd, status, decided_at, venue_a, venue_b) VALUES (888003,'PWUSDT','test',0,0,'unqualified',now(),'binance','okx')`); err != nil {
		t.Fatalf("seed unqualified: %v", err)
	}

	loads, err := st.PipelineFunnelBacklog(ctx)
	if err != nil {
		t.Fatalf("PipelineFunnelBacklog: %v", err)
	}
	byStage := map[string]FunnelStageLoad{}
	for _, l := range loads {
		byStage[l.Stage] = l
	}
	if len(loads) != 3 {
		t.Fatalf("want 3 stages, got %d", len(loads))
	}
	if l := byStage["hypothesis_ruling"]; l.Count != 2 {
		t.Errorf("hypothesis_ruling count=%d want 2（approved/rejected 不入计）", l.Count)
	} else if !l.Oldest.Truncate(time.Minute).Equal(old.Truncate(time.Minute)) {
		t.Errorf("hypothesis_ruling oldest=%v want %v", l.Oldest, old)
	}
	if l := byStage["proposal_ruling"]; l.Count != 1 {
		t.Errorf("proposal_ruling count=%d want 1（expired 不入计）", l.Count)
	}
	if l := byStage["graduation_eval"]; l.Count != 3 {
		t.Errorf("graduation_eval count=%d want 3（unqualified 不入计）", l.Count)
	} else if l.Oldest == nil || l.Oldest.Truncate(time.Minute).Equal(old.Truncate(time.Minute)) == false {
		t.Errorf("graduation_eval oldest=%v want %v", l.Oldest, old)
	}

	// 清场：零行段 Count=0+Oldest=nil。
	if _, err := st.Pool().Exec(ctx, `DELETE FROM carry_graduations; DELETE FROM paper_spot_carry; DELETE FROM fin_opp_hypothesis; DELETE FROM fin_proposal`); err != nil {
		t.Fatalf("clear tables 2: %v", err)
	}
	loads, err = st.PipelineFunnelBacklog(ctx)
	if err != nil {
		t.Fatalf("PipelineFunnelBacklog empty: %v", err)
	}
	for _, l := range loads {
		if l.Count != 0 || l.Oldest != nil {
			t.Errorf("stage %s want empty (count=0 oldest=nil), got count=%d oldest=%v", l.Stage, l.Count, l.Oldest)
		}
	}
}
