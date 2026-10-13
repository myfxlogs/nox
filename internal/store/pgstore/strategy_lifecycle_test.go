// strategy_lifecycle_test.go —— D-260 M4.2 锚测（5434 实库）：
// 覆盖/状态机投影/判据表机读/SLA 过期/首晋级告警。
package pgstore

import (
	"context"
	"testing"
	"time"
)

func TestM42_FinStrategyCoverage(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	// 迁移 1009 + EnsureStrategyRows 已放：先跑一次覆盖闸（共享库可能有
	// 并行测试后插 stats 行——Ensure 后再断言）。
	if _, err := s.EnsureStrategyRows(ctx, nil); err != nil {
		t.Fatal(err)
	}
	var missing int
	if err := s.pool.QueryRow(ctx, `
		SELECT count(*) FROM strategy_stats ss
		WHERE NOT EXISTS (SELECT 1 FROM fin_strategy fs WHERE fs.strategy=ss.strategy)`).Scan(&missing); err != nil {
		t.Fatal(err)
	}
	if missing > 0 {
		t.Fatalf("fin_strategy 覆盖缺 %d 行", missing)
	}
	// 模板键补行幂等（残留态先清保断言可复跑）。
	if _, err := s.pool.Exec(ctx, `DELETE FROM fin_strategy WHERE strategy='t_m42_tmpl'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `DELETE FROM strategy_stats WHERE strategy='t_m42_tmpl'`); err != nil {
		t.Fatal(err)
	}
	if n, err := s.EnsureStrategyRows(ctx, []string{"t_m42_tmpl"}); err != nil {
		t.Fatal(err)
	} else if n != 1 {
		t.Fatalf("EnsureStrategyRows n=%d want 1（模板键新行）", n)
	}
	st, ok, err := s.GetStrategyState(ctx, "t_m42_tmpl")
	if err != nil || !ok || st != "candidate" {
		t.Fatalf("新模板键 state=%q ok=%v want candidate（fail-closed 起点）", st, ok)
	}
	if n, err := s.EnsureStrategyRows(ctx, []string{"t_m42_tmpl"}); err != nil || n != 0 {
		t.Fatalf("EnsureStrategyRows 复跑 n=%d want 0（幂等）", n)
	}
}

func TestM42_StateProjectionDisabled(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	// 清残留（共享库可复跑）：确保从 candidate 起步。
	if _, err := s.pool.Exec(ctx,
		`DELETE FROM fin_strategy WHERE strategy='t_m42_proj';
		 DELETE FROM strategy_stats WHERE strategy='t_m42_proj';
		 DELETE FROM alerts WHERE message LIKE '%t_m42_proj%'`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.EnsureStrategyRows(ctx, []string{"t_m42_proj"}); err != nil {
		t.Fatal(err)
	}
	// candidate→disabled=true（非 live 即停）。
	d, err := s.IsStrategyDisabled(ctx, "t_m42_proj")
	if err != nil || !d {
		t.Fatalf("candidate disabled=%v want true", d)
	}
	// 晋级 live→disabled=false + strategy_stats 同步。
	if err := s.SetStrategyState(ctx, "t_m42_proj", "live", "committee", nil); err != nil {
		t.Fatal(err)
	}
	if d, err := s.IsStrategyDisabled(ctx, "t_m42_proj"); err != nil || d {
		t.Fatalf("live disabled=%v want false", d)
	}
	var sd bool
	if err := s.pool.QueryRow(ctx, `SELECT disabled FROM strategy_stats WHERE strategy='t_m42_proj'`).Scan(&sd); err != nil || sd {
		t.Fatalf("strategy_stats.disabled=%v want false（live 投影同步）", sd)
	}
	// 晋级告警落行（首次 live=info）。
	var alerts int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '%t_m42_proj%晋级 live%'`).Scan(&alerts); err != nil {
		t.Fatal(err)
	}
	if alerts == 0 {
		t.Fatal("首次晋级 live 告警缺")
	}
	// 回退 demoted→disabled=true。
	if err := s.SetStrategyState(ctx, "t_m42_proj", "demoted", "committee", []string{"manual"}); err != nil {
		t.Fatal(err)
	}
	if d, err := s.IsStrategyDisabled(ctx, "t_m42_proj"); err != nil || !d {
		t.Fatal("demoted want disabled")
	}
	dis, err := s.ListDisabledStrategies(ctx)
	if err != nil || !dis["t_m42_proj"] {
		t.Fatal("ListDisabledStrategies 状态机投影缺")
	}
	// 行缺回退 strategy_stats.disabled（未登记策略兼容）。
	if d, err := s.IsStrategyDisabled(ctx, "t_m42_nonexistent"); err != nil || d {
		t.Fatal("未登记策略 disabled=false（无 fin_strategy 行且 stats 无行）")
	}
}

func TestM42_CriteriaMachineReadable(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	rows, err := s.ListStrategyCriteria(ctx, "*", "elimination")
	if err != nil || len(rows) < 2 {
		t.Fatalf("elimination 判据种子缺: %d %v", len(rows), err)
	}
	// 改行即改判据（验收③）：把大样本胜率线改 0.9→winRate=0.5 也点火；
	// 本测只证读面→改值回写生效。测试完还原。
	if _, err := s.pool.Exec(ctx, `
		UPDATE fin_strategy_criteria SET params=jsonb_set(params,'{conds}','["win_rate<0.9","avg_pnl<0"]')
		WHERE pred='large_sample_kill'`); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = s.pool.Exec(ctx, `
			UPDATE fin_strategy_criteria SET params=jsonb_set(params,'{conds}','["win_rate<0.3","avg_pnl<0"]')
			WHERE pred='large_sample_kill'`)
	}()
	rows2, _ := s.ListStrategyCriteria(ctx, "*", "elimination")
	found := false
	for _, r := range rows2 {
		if r.Pred == "large_sample_kill" {
			for _, c := range r.Params["conds"].([]any) {
				if c == "win_rate<0.9" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatal("判据改行未生效（读面未回改值）")
	}
}

func TestM42_ProposalSLA(t *testing.T) {
	s := sharedTestStore(t)
	ctx := context.Background()
	// 超龄 proposed→expired+id 回；非 proposed 不动；重跑幂等零。
	id, _, err := s.CreateProposal(ctx, &Proposal{TemplateID: "t_m42_sla", ExecTrack: "auto", Venue: "binance"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.pool.Exec(ctx, `UPDATE fin_proposal SET created_at=now()-interval '30 hours' WHERE id=$1`, id); err != nil {
		t.Fatal(err)
	}
	ids, err := s.ExpireProposalsOverAge(ctx, 24*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	hit := false
	for _, x := range ids {
		if x == id {
			hit = true
		}
	}
	if !hit {
		t.Fatalf("SLA 过期未含 #%d", id)
	}
	var st string
	if err := s.pool.QueryRow(ctx, `SELECT status FROM fin_proposal WHERE id=$1`, id).Scan(&st); err != nil || st != "expired" {
		t.Fatalf("status=%q want expired", st)
	}
	// 幂等重跑不再含该行。
	ids2, _ := s.ExpireProposalsOverAge(ctx, 24*time.Hour)
	for _, x := range ids2 {
		if x == id {
			t.Fatal("expired 行重复过期（非幂等）")
		}
	}
	// SLA 键机读全链：写入→回读生效（改值=改行为）。共享库键可能被并行测试
	// 删改——Upsert 自证读写链（迁移种子值已由 CriteriaMachineReadable 侧证表在）。
	if err := s.UpsertAppConfig(ctx, "proposal_sla_hours", "24", "m42 test"); err != nil {
		t.Fatal(err)
	}
	v, ok, err := s.AppConfigFloat(ctx, "proposal_sla_hours")
	if err != nil || !ok || v != 24 {
		t.Fatalf("proposal_sla_hours=%v ok=%v err=%v want 24", v, ok, err)
	}
}
