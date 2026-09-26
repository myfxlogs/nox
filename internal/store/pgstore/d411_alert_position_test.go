// d411_alert_position_test.go —— D-411 S4：alerts.position_id 列与持仓域告警静默失败修复。
// 红锚：旧路径 InsertAlert(&posID) FK→rules 违规必返回 error（生产缺陷的可测复现）；
// 新写口 InsertPositionAlert 落 position_id（rule_id=NULL），ListAlerts/proto 透出。
package pgstore

import (
	"context"
	"testing"

	"arb/internal/testutil"
)

func d411Store(t *testing.T) *Store {
	t.Helper()
	s, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.pool.Exec(context.Background(),
		`TRUNCATE alerts, positions RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	return s
}

func TestD411_PositionAlert(t *testing.T) {
	ctx := context.Background()
	s := d411Store(t)
	posID, err := s.CreatePosition(ctx, &Position{
		OpportunityID: "D411:test:opp", Type: "XASSET_CARRY", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}

	// D-496：rules 表与 alerts.rule_id 列已删——旧路径参数忽略、行照常落库
	// （rule_id 语义不再存在，曾经的静默丢告警病灶由 schema 层根治）。
	if err := s.InsertAlert(ctx, &posID, "critical", "old-path-posid-as-ruleid"); err != nil {
		t.Fatalf("旧路径不再依赖 rules FK，应成功: %v", err)
	}
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE position_id IS NULL AND message='old-path-posid-as-ruleid'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("旧路径应落行且 position_id=NULL, n=%d err=%v", n, err)
	}

	// 新写口：position_id=持仓、rule_id IS NULL。
	if err := s.InsertPositionAlert(ctx, posID, "critical", "pos-alert"); err != nil {
		t.Fatalf("InsertPositionAlert: %v", err)
	}
	var gotPos, gotRule *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT position_id, NULL::bigint FROM alerts WHERE message='pos-alert'`).Scan(&gotPos, &gotRule); err != nil {
		t.Fatalf("query alert: %v", err)
	}
	if gotPos == nil || *gotPos != posID || gotRule != nil {
		t.Fatalf("position_id=%v rule_id=%v want posID=%d/nil", gotPos, gotRule, posID)
	}

	// ListAlerts 读口透出 position_id（旧路径行+持仓行共 2 条）。
	rows, err := s.ListAlerts(ctx, 10, 0)
	if err != nil || len(rows) != 2 {
		t.Fatalf("ListAlerts: %d %v", len(rows), err)
	}
	var posRow *Alert
	for i := range rows {
		if rows[i].Message == "pos-alert" {
			posRow = &rows[i]
		}
	}
	if posRow == nil || posRow.PositionID == nil || *posRow.PositionID != posID || posRow.RuleID != nil {
		t.Fatalf("ListAlerts pos-alert row missing/misbound")
	}

	// 系统级告警（rule_id=nil 语义）行为不变回归。
	if err := s.InsertAlert(ctx, nil, "warn", "sys-alert"); err != nil {
		t.Fatalf("InsertAlert nil: %v", err)
	}
	var sysPos, sysRule *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT position_id, NULL::bigint FROM alerts WHERE message='sys-alert'`).Scan(&sysPos, &sysRule); err != nil {
		t.Fatalf("query sys: %v", err)
	}
	if sysPos != nil || sysRule != nil {
		t.Fatalf("系统级应双 NULL, pos=%v rule=%v", sysPos, sysRule)
	}
}
