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

	// 红锚（缺陷复现）：旧路径把 posID 塞 rule_id → FK→rules 违规 → error。
	// 生产上该 error 被 `_ =` 吞净 = 告警静默丢失（981 行 rule_id 全 NULL 实证）。
	if err := s.InsertAlert(ctx, &posID, "critical", "old-path-posid-as-ruleid"); err == nil {
		t.Fatal("旧路径应 FK 违规返回 error（无对应 rules 行）")
	}
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM alerts`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("旧路径不应落行, n=%d err=%v", n, err)
	}

	// 新写口：position_id=持仓、rule_id IS NULL。
	if err := s.InsertPositionAlert(ctx, posID, "critical", "pos-alert"); err != nil {
		t.Fatalf("InsertPositionAlert: %v", err)
	}
	var gotPos, gotRule *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT position_id, rule_id FROM alerts WHERE message='pos-alert'`).Scan(&gotPos, &gotRule); err != nil {
		t.Fatalf("query alert: %v", err)
	}
	if gotPos == nil || *gotPos != posID || gotRule != nil {
		t.Fatalf("position_id=%v rule_id=%v want posID=%d/nil", gotPos, gotRule, posID)
	}

	// ListAlerts 读口透出 position_id。
	rows, err := s.ListAlerts(ctx, 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("ListAlerts: %d %v", len(rows), err)
	}
	if rows[0].PositionID == nil || *rows[0].PositionID != posID || rows[0].RuleID != nil {
		t.Fatalf("ListAlerts row: posID=%v ruleID=%v", rows[0].PositionID, rows[0].RuleID)
	}

	// 系统级告警（rule_id=nil 语义）行为不变回归。
	if err := s.InsertAlert(ctx, nil, "warn", "sys-alert"); err != nil {
		t.Fatalf("InsertAlert nil: %v", err)
	}
	var sysPos, sysRule *int64
	if err := s.pool.QueryRow(ctx,
		`SELECT position_id, rule_id FROM alerts WHERE message='sys-alert'`).Scan(&sysPos, &sysRule); err != nil {
		t.Fatalf("query sys: %v", err)
	}
	if sysPos != nil || sysRule != nil {
		t.Fatalf("系统级应双 NULL, pos=%v rule=%v", sysPos, sysRule)
	}
}
