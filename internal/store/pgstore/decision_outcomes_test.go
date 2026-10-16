package pgstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// DB 集成测试：裁定归因簿（D-561）——物化落行/30d 归因值/连败 warn 边沿三断言。
// 需 NOX_TEST_PG_DSN，未设置时 skip（testStore 门禁）。
// 共享库纪律：fixture 走 d561 前缀唯一键 + 定点 DELETE 清理（禁 TRUNCATE 共享表——
// go test 跨包并行，清表打翻他包在途 fixture，实证 4 测连坐）。

const (
	d561Struct   = "d561_struct"
	d561Strategy = "d561_strategy"
	d561Symbol   = "D561USDT"
	d561Template = "d561_template"
	d561PaperID  = int64(990047)
)

func d561CleanupStmts() []string {
	return []string{
		fmt.Sprintf(`DELETE FROM carry_graduations WHERE paper_id=%d`, d561PaperID),
		fmt.Sprintf(`DELETE FROM paper_spot_carry WHERE id=%d`, d561PaperID),
		fmt.Sprintf(`DELETE FROM fin_proposal WHERE template_id='%s'`, d561Template),
		`DELETE FROM fin_position_strategy_map WHERE source_val LIKE 'd561%'`,
		`DELETE FROM positions WHERE opportunity_id LIKE '%D561USDT%'`,
		`DELETE FROM alerts WHERE message LIKE '%D561USDT%'`,
		`DELETE FROM fin_decision_outcomes WHERE strategy_key LIKE 'd561%'`,
	}
}

func d561Seed(t *testing.T, s *Store, ctx context.Context, now time.Time) (gradObjectKey string) {
	t.Helper()
	for _, q := range d561CleanupStmts() {
		if _, err := s.pool.Exec(ctx, q); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
	t.Cleanup(func() {
		for _, q := range d561CleanupStmts() {
			_, _ = s.pool.Exec(context.Background(), q)
		}
	})
	// 纸面行（graduation FK 父行）。
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO paper_spot_carry (id, symbol, side, entry_funding_ann, qty_usd, venue_a)
		VALUES ($1, $2, 'long', 0.15, 5000, 'okx')`, d561PaperID, d561Symbol); err != nil {
		t.Fatalf("seed paper: %v", err)
	}
	// 判据表：structure→template（D-554 判据表面复刻，键独占）。
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO fin_position_strategy_map (source_kind, source_val, strategy_key)
		 VALUES ('structure', $1, $2)`, d561Struct, d561Strategy); err != nil {
		t.Fatalf("seed map: %v", err)
	}
	// 已批毕业（decided_by=''=人工 RPC；8d 前裁决）。
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO carry_graduations (paper_id, symbol, structure, age_days, accrued_usd, status,
		                               decided_at, decided_by, combo_id, decided_note, venue_a, venue_b)
		VALUES ($1, $2, $3, 30, 12.5, 'approved', $4, '', 901, 'D-550 授权', 'okx', '3001157')`,
		d561PaperID, d561Symbol, d561Struct, now.Add(-8*24*time.Hour)); err != nil {
		t.Fatalf("seed grad: %v", err)
	}
	var gradID int64
	if err := s.pool.QueryRow(ctx,
		`SELECT id FROM carry_graduations WHERE paper_id=$1`, d561PaperID).Scan(&gradID); err != nil {
		t.Fatalf("read grad id: %v", err)
	}
	// 已执行提案（dciexec 推进，2d 前）。
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO fin_proposal (template_id, exec_track, venue, status, decided_at)
		VALUES ($1, 'auto', 'okx', 'executed', $2)`, d561Template, now.Add(-2*24*time.Hour)); err != nil {
		t.Fatalf("seed proposal: %v", err)
	}
	// 毕业其后真金仓×结算：净 +2 / −1 / −1 / −1（settled_at 倒序连续三负=连败 3）。
	// 第 5 仓=窗外对照（decided_at 前开仓）；第 6 仓=canonical 不匹配对照。
	type posRow struct {
		oppID     string
		openedAt  time.Time
		net       *float64
		settledAt *time.Time
	}
	neg, pos2 := -1.0, 2.0
	tA := now.Add(-24 * time.Hour)
	tB := now.Add(-48 * time.Hour)
	tC := now.Add(-72 * time.Hour)
	tD := now.Add(-96 * time.Hour)
	before := now.Add(-9 * 24 * time.Hour)
	rows := []posRow{
		{"SWAPARB:D561USDT:okx:mt5:x", now.Add(-7 * 24 * time.Hour), &pos2, &tD},
		{"SWAPARB:D561USDT:okx:mt5:x", now.Add(-6 * 24 * time.Hour), &neg, &tC},
		{"SWAPARB:D561USDT:okx:mt5:x", now.Add(-5 * 24 * time.Hour), &neg, &tB},
		{"SWAPARB:D561USDT:okx:mt5:x", now.Add(-4 * 24 * time.Hour), &neg, &tA},
		{"SWAPARB:D561USDT:okx:mt5:x", before, &neg, &before},           // 窗外（decided_at 前开仓）
		{"CARRY:XAUUSD:a:b:x", now.Add(-6 * 24 * time.Hour), &neg, &tB}, // canonical 不匹配
	}
	for i, r := range rows {
		var pid int64
		if err := s.pool.QueryRow(ctx, `
			INSERT INTO positions (opportunity_id, type, status, opened_at, strategy_key)
			VALUES ($1,'SWAPARB','CLOSED',$2,$3) RETURNING id`,
			r.oppID, r.openedAt, d561Strategy).Scan(&pid); err != nil {
			t.Fatalf("seed position %d: %v", i, err)
		}
		if r.net != nil {
			if _, err := s.pool.Exec(ctx, `
				INSERT INTO position_settlements (position_id, net_realized_usd, settled_at, completeness_status)
				VALUES ($1,$2,$3,'complete')`, pid, *r.net, *r.settledAt); err != nil {
				t.Fatalf("seed settlement %d: %v", i, err)
			}
		}
	}
	return fmt.Sprintf("grad:%d", gradID)
}

func d561Outcome(t *testing.T, s *Store, objectKey string) *DecisionOutcome {
	t.Helper()
	outcomes, err := s.ListDecisionOutcomes(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for i := range outcomes {
		if outcomes[i].ObjectKey == objectKey {
			return &outcomes[i]
		}
	}
	t.Fatalf("outcome %s 缺行", objectKey)
	return nil
}

func TestDecisionOutcomesMaterializeAndAttrib(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	gradKey := d561Seed(t, s, ctx, now)

	// 断言①：物化落行（毕业+提案；UNIQUE 幂等重跑零重落）。
	for range 2 {
		if _, err := s.MaterializeDecisionOutcomes(ctx); err != nil {
			t.Fatalf("materialize: %v", err)
		}
	}
	grad := d561Outcome(t, s, gradKey)
	if grad.StrategyKey != d561Strategy || grad.Canonical != d561Symbol || grad.DecisionRef != "committee_rpc" {
		t.Errorf("毕业行字段: %+v", grad)
	}
	outcomes, err := s.ListDecisionOutcomes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var propKey string
	for _, o := range outcomes {
		if o.Category == "proposal_go" && o.StrategyKey == d561Template {
			propKey = o.ObjectKey
		}
	}
	if propKey == "" {
		t.Fatal("提案行缺行（executed 提案未物化）")
	}
	if prop := d561Outcome(t, s, propKey); prop.DecisionRef != "dciexec" {
		t.Errorf("提案行 decision_ref: got %q, want dciexec", prop.DecisionRef)
	}

	// 断言②+③：归因值（net=-1，4 仓窗内，1 胜 3 负，连败 3）+ 边沿告警（首刷 1 条，二刷 0）。
	alerts, err := s.RefreshDecisionOutcomeAttribution(ctx)
	if err != nil {
		t.Fatalf("refresh: %v", err)
	}
	grad = d561Outcome(t, s, gradKey)
	if *grad.AttribNetUSD != -1 || *grad.AttribPos != 4 || *grad.AttribWins != 1 || *grad.AttribLosses != 3 || *grad.AttribStreak != 3 {
		t.Errorf("归因值: net=%v pos=%d wins=%d losses=%d streak=%d, want -1/4/1/3/3（窗外+异 canonical 剔除）",
			*grad.AttribNetUSD, *grad.AttribPos, *grad.AttribWins, *grad.AttribLosses, *grad.AttribStreak)
	}
	if grad.AlertState != "net_loss+streak" {
		t.Errorf("告警态: got %q, want net_loss+streak", grad.AlertState)
	}
	var myAlerts int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM alerts WHERE level='warn' AND message LIKE '%D561USDT%'`).Scan(&myAlerts); err != nil {
		t.Fatalf("count alerts: %v", err)
	}
	if myAlerts != 1 {
		t.Errorf("首刷边沿 warn 行: got %d, want 1", myAlerts)
	}
	if alerts == 0 && myAlerts == 1 {
		t.Log("refresh 计数被他包并发告警稀释（跨包共享库），以行数断言为准")
	}
	if alerts2, err := s.RefreshDecisionOutcomeAttribution(ctx); err != nil {
		t.Fatalf("refresh2: %v", err)
	} else if alerts2 != 0 {
		t.Errorf("二刷边沿: got %d, want 0（同态不重推）", alerts2)
	}
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM alerts WHERE level='warn' AND message LIKE '%D561USDT%'`).Scan(&myAlerts); err != nil || myAlerts != 1 {
		t.Errorf("warn 告警行终态: got %d err=%v, want 1", myAlerts, err)
	}
}
