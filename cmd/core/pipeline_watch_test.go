package main

// 管线堵点自盯锚（§6.1③ 系统自盯版）：
// ①纯函数判定边界——积压/停滞双阈严格大于（等值不触发）、单条件不触发、零行段不判；
// ②thresholds app_config 覆写生效（键缺→缺省、键在→覆写）；
// ③Poll 端到端——超阈段产 warn 告警+dedup 二跑零重报。

import (
	"context"
	"fmt"
	"testing"
	"time"

	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func TestPipelineBreachesBoundaries(t *testing.T) {
	now := time.Now()
	old := now.Add(-20 * 24 * time.Hour)
	fresh := now.Add(-1 * time.Hour)
	loads := []pgstore.FunnelStageLoad{
		{Stage: "hypothesis_ruling", Count: 11, Oldest: &old}, // 双超→中
		{Stage: "proposal_ruling", Count: 10, Oldest: &old},   // 积压等值→不触发（严格大于）
		{Stage: "graduation_eval", Count: 12, Oldest: &fresh}, // 停滞未超→不触发（AND 语义）
		{Stage: "hypothesis_ruling", Count: 11, Oldest: nil},  // 零行段不判（构造性：nil oldest）
	}
	got := pipelineBreaches(loads, 10, 7, now)
	if len(got) != 1 || got[0].Stage != "hypothesis_ruling" || got[0].Count != 11 {
		t.Fatalf("want only hypothesis_ruling(11,old), got %+v", got)
	}
	// 零行段：Count=0+Oldest=nil 不触发。
	if out := pipelineBreaches([]pgstore.FunnelStageLoad{{Stage: "s", Count: 0}}, 10, 7, now); len(out) != 0 {
		t.Fatalf("empty stage must not breach, got %+v", out)
	}
}

func TestPipelineWatchThresholdsOverride(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	c := &pipelineWatchCollector{st: st}
	// 键缺→缺省（共享库跨跑持久——先清两键保证缺席前置）。
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM app_config WHERE key IN ('pipeline_backlog_warn','pipeline_stall_days_warn')`); err != nil {
		t.Fatalf("clear config keys: %v", err)
	}
	b, s, err := c.thresholds(ctx)
	if err != nil {
		t.Fatalf("thresholds (absent): %v", err)
	}
	if b != pipelineBacklogWarnDefault || s != pipelineStallDaysWarnDefault {
		t.Fatalf("absent keys want defaults %d/%d, got %d/%d",
			pipelineBacklogWarnDefault, pipelineStallDaysWarnDefault, b, s)
	}
	// 键在→覆写。
	for _, kv := range [][2]string{{"pipeline_backlog_warn", "25"}, {"pipeline_stall_days_warn", "3"}} {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO app_config (key, value) VALUES ($1,$2) ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`, kv[0], kv[1]); err != nil {
			t.Fatalf("seed %s: %v", kv[0], err)
		}
	}
	b, s, err = c.thresholds(ctx)
	if err != nil {
		t.Fatalf("thresholds (override): %v", err)
	}
	if b != 25 || s != 3 {
		t.Fatalf("override want 25/3, got %d/%d", b, s)
	}
}

func TestPipelineWatchCollectorPollAlertDedup(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(st.Close)
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM fin_opp_hypothesis; DELETE FROM fin_proposal; DELETE FROM carry_graduations; DELETE FROM alerts`); err != nil {
		t.Fatalf("clear tables: %v", err)
	}
	// 同包先行测试会写阈值键（共享 app_config 跨测试持久）——清键保缺省阈 10/7。
	if _, err := st.Pool().Exec(ctx,
		`DELETE FROM app_config WHERE key IN ('pipeline_backlog_warn','pipeline_stall_days_warn')`); err != nil {
		t.Fatalf("clear config keys: %v", err)
	}
	// 单段超双阈：11 行 proposed、最老 20d（阈 10/7）。
	old := time.Now().Add(-20 * 24 * time.Hour)
	for i := 0; i < 11; i++ {
		if _, err := st.Pool().Exec(ctx,
			`INSERT INTO fin_opp_hypothesis (venue, category_key, status, first_seen) VALUES ('gate',$1,'proposed',$2)`,
			fmt.Sprintf("PW_H_%02d", i), old); err != nil {
			t.Fatalf("seed hypothesis %d: %v", i, err)
		}
	}
	c := &pipelineWatchCollector{st: st}
	if facts, err := c.Poll(ctx); err != nil {
		t.Fatalf("poll: %v", err)
	} else if len(facts) != 0 {
		t.Fatalf("collector must produce zero facts, got %d", len(facts))
	}
	alertN := func() int {
		var n int
		if err := st.Pool().QueryRow(ctx,
			`SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '[管线堵点]%hypothesis_ruling%'`).Scan(&n); err != nil {
			t.Fatalf("count alerts: %v", err)
		}
		return n
	}
	if n := alertN(); n != 1 {
		t.Fatalf("first poll want 1 alert, got %d", n)
	}
	// 二跑 dedup：同报文零重报。
	if _, err := c.Poll(ctx); err != nil {
		t.Fatalf("poll 2: %v", err)
	}
	if n := alertN(); n != 1 {
		t.Fatalf("second poll must dedup (still 1 alert), got %d", n)
	}
	// 阈值抬高到积压之上→不再产新告警（解除路径）。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO app_config (key, value) VALUES ('pipeline_backlog_warn','100') ON CONFLICT (key) DO UPDATE SET value=EXCLUDED.value`); err != nil {
		t.Fatalf("raise threshold: %v", err)
	}
	if _, err := c.Poll(ctx); err != nil {
		t.Fatalf("poll 3: %v", err)
	}
	if n := alertN(); n != 1 {
		t.Fatalf("raised threshold must stop new alerts, got %d", n)
	}
}
