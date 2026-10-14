package main

import (
	"context"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// TestDeadCorner_DaemonZeroOutput 票面锚：注入「running 且 beat 新鲜」daemon +
// 产出域零行 → 报面出现其名；running=false 的不当死角（离线面另有滞跳报）。
func TestDeadCorner_DaemonZeroOutput(t *testing.T) {
	// 自足库：digest 独立模块无 testutil 建库件——postgres 库落 digest_dead_test
	// + 三面消费的最小表集（loadDeadCorners 触达面全集），表存在与否不赌共享库态。
	dsn := os.Getenv("NOX_TEST_PG_DSN")
	if dsn == "" {
		dsn = "postgres://nox:nox@127.0.0.1:5434/digest_dead_test"
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, "postgres://nox:nox@127.0.0.1:5434/postgres")
	if err != nil {
		t.Skipf("test db admin unavailable: %v", err)
	}
	var exists bool
	if err := admin.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM pg_database WHERE datname='digest_dead_test')`).Scan(&exists); err != nil {
		t.Skipf("test db probe: %v", err)
	}
	if !exists {
		if _, err := admin.Exec(ctx, `CREATE DATABASE digest_dead_test`); err != nil {
			t.Skipf("create digest_dead_test: %v", err)
		}
	}
	admin.Close()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("test db unavailable: %v", err)
	}
	defer pool.Close()
	for _, q := range []string{
		`CREATE TABLE IF NOT EXISTS fin_employee_registry (name text PRIMARY KEY, cadence_sec int NOT NULL DEFAULT 0, running bool NOT NULL DEFAULT false, beat_ts timestamptz, enabled bool NOT NULL DEFAULT true)`,
		`CREATE TABLE IF NOT EXISTS facts (kind text NOT NULL, venue text NOT NULL, value float8, ts timestamptz)`,
		`CREATE TABLE IF NOT EXISTS fin_strategy (strategy text PRIMARY KEY, state text NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS autotrade_logs (strategy text, ts timestamptz)`,
		`CREATE TABLE IF NOT EXISTS fin_proposal (template_id text, created_at timestamptz)`,
		`CREATE TABLE IF NOT EXISTS fin_cex_earn_apr (ts timestamptz)`,
		`CREATE TABLE IF NOT EXISTS fin_venue_account_snapshot (fetched_at timestamptz)`,
		`CREATE TABLE IF NOT EXISTS ticks (ts timestamptz)`,
		`CREATE TABLE IF NOT EXISTS income_events (venue text, ts timestamptz)`,
		`CREATE TABLE IF NOT EXISTS fin_cex_universe (last_seen timestamptz)`,
		`CREATE TABLE IF NOT EXISTS position_cashflows (ts timestamptz)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("bootstrap schema: %v", err)
		}
	}
	for _, q := range []string{
		`DELETE FROM fin_employee_registry WHERE name LIKE 'xgdc%'`,
		`DELETE FROM facts WHERE kind LIKE 'xgdc%'`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("cleanup: %v", err)
		}
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM fin_employee_registry WHERE name LIKE 'xgdc%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM facts WHERE kind LIKE 'xgdc%'`)
	}()
	// 在职 daemon（产出域 facts xgdc_dead 零行）+ 离线 daemon（产出域 xgdc_off 零行但不当列）。
	if _, err := pool.Exec(ctx, `INSERT INTO fin_employee_registry
		(name, cadence_sec, running, beat_ts, enabled) VALUES
		('xgdc-mon', 60, true, now(), true),
		('xgdc-off', 60, false, now(), true)`); err != nil {
		t.Fatalf("seed employees: %v", err)
	}
	// 孤儿 facts kind（无消费件在案）。
	if _, err := pool.Exec(ctx, `INSERT INTO facts (kind, venue, value, ts)
		VALUES ('xgdc_orphan','xgdc',1,now())`); err != nil {
		t.Fatalf("seed fact: %v", err)
	}
	// C 面：live 零动作列死角；live 有近期动作不列。
	if _, err := pool.Exec(ctx, `INSERT INTO fin_strategy (strategy, state) VALUES
		('xgdc_dead_strat','live'), ('xgdc_live_strat','live')`); err != nil {
		t.Fatalf("seed strategies: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO autotrade_logs (strategy, ts)
		VALUES ('xgdc_live_strat', now())`); err != nil {
		t.Fatalf("seed autotrade log: %v", err)
	}
	defer func() {
		_, _ = pool.Exec(ctx, `DELETE FROM fin_strategy WHERE strategy LIKE 'xgdc%'`)
		_, _ = pool.Exec(ctx, `DELETE FROM autotrade_logs WHERE strategy LIKE 'xgdc%'`)
	}()
	daemonOutputs = append(daemonOutputs,
		deadOut{Daemon: "xgdc-mon", Table: "facts", TsCol: "ts", Where: "kind='xgdc_dead'"},
		deadOut{Daemon: "xgdc-off", Table: "facts", TsCol: "ts", Where: "kind='xgdc_off'"})
	defer func() { daemonOutputs = daemonOutputs[:len(daemonOutputs)-2] }()

	cs, err := loadDeadCorners(ctx, pool)
	if err != nil {
		t.Fatalf("loadDeadCorners: %v", err)
	}
	var mon, off, orphan, deadStrat, liveStrat bool
	for _, c := range cs {
		switch c.Name {
		case "xgdc-mon":
			mon = true
		case "xgdc-off":
			off = true
		case "xgdc_orphan":
			orphan = true
		case "xgdc_dead_strat":
			deadStrat = true
		case "xgdc_live_strat":
			liveStrat = true
		}
	}
	if !mon {
		t.Fatal("在职 daemon 产出域 24h 零新行应列死角")
	}
	if off {
		t.Fatal("离线 daemon 不应列死角（滞跳面另有报）")
	}
	if !orphan {
		t.Fatal("无消费件的 fresh facts kind 应列孤儿")
	}
	if !deadStrat || liveStrat {
		t.Fatalf("策略面判错: deadStrat=%v(要true) liveStrat=%v(要false)", deadStrat, liveStrat)
	}
	md := renderDeadCornerSection(cs)
	if !strings.Contains(md, "死角：") || !strings.Contains(md, "xgdc-mon") {
		t.Fatalf("段渲染缺名: %s", md)
	}
}

// TestDeadCorner_RenderZero N=0 照常印一行（票面钦定防静默）。
func TestDeadCorner_RenderZero(t *testing.T) {
	md := renderDeadCornerSection(nil)
	if !strings.Contains(md, "死角：0") {
		t.Fatalf("N=0 段缺行: %s", md)
	}
}

// 裁决队列浮面锚：proposed 计数+分类键 top3；N=0 照常印行。
func TestAdjudicationQueue(t *testing.T) {
	dsn := os.Getenv("NOX_TEST_DSN")
	if dsn == "" {
		dsn = "postgres://nox:nox@127.0.0.1:5434/digest_dead_test"
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("pg: %v", err)
	}
	defer pool.Close()
	if _, err := pool.Exec(ctx,
		`CREATE TABLE IF NOT EXISTS fin_opp_hypothesis (category_key text, status text)`); err != nil {
		t.Fatalf("schema: %v", err)
	}
	pool.Exec(ctx, `DELETE FROM fin_opp_hypothesis WHERE category_key LIKE 'xadq%'`)
	defer pool.Exec(ctx, `DELETE FROM fin_opp_hypothesis WHERE category_key LIKE 'xadq%'`)
	if _, err := pool.Exec(ctx, `INSERT INTO fin_opp_hypothesis (category_key, status) VALUES
		('xadq_a|v|s','proposed'),('xadq_a|v|s2','proposed'),('xadq_b|v|s','proposed'),('xadq_a|v|s3','approved')`); err != nil {
		t.Fatal(err)
	}
	n, tops, err := loadAdjudicationQueue(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if n < 3 || len(tops) == 0 || tops[0] != "xadq_a×2" {
		t.Fatalf("n=%d tops=%v", n, tops)
	}
	if out := renderAdjudicationQueue(n, tops); !strings.Contains(out, "待裁假设") || !strings.Contains(out, "xadq_a×2") {
		t.Fatalf("render: %s", out)
	}
	if out := renderAdjudicationQueue(0, nil); !strings.Contains(out, "待裁假设：0") {
		t.Fatalf("N=0 照常印行: %s", out)
	}
}
