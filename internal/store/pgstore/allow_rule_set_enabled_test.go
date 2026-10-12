// allow_rules_d444_test.go —— D-444 T1：SetAutotradeAllowRuleEnabled 翻转+note+RETURNING
// 回读；不存在 id→error；ListAutotradeAllowRulesAll 含 disabled。需 NOX_TEST_PG_DSN。
package pgstore

import (
	"context"
	"testing"

	"nox/internal/platform/testutil"
)

func d444Store(t *testing.T) *Store {
	t.Helper()
	s, err := New(context.Background(), testutil.TestDSN(t, "NOX_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if _, err := s.pool.Exec(context.Background(),
		`DELETE FROM autotrade_allow_rules WHERE value LIKE 'd444test%'`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	return s
}

func TestD444AllowRuleSetEnabled(t *testing.T) {
	s := d444Store(t)
	ctx := context.Background()

	var id int64
	if err := s.pool.QueryRow(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated, note)
		VALUES ('venue','d444test:mt5',true,false,'orig') RETURNING id`).Scan(&id); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// 翻转 disabled + note 覆写，RETURNING 行回读
	r, err := s.SetAutotradeAllowRuleEnabled(ctx, id, false, "d444 flip")
	if err != nil {
		t.Fatalf("set: %v", err)
	}
	if r.Enabled || r.Note != "d444 flip" || r.Value != "d444test:mt5" || r.Kind != "venue" {
		t.Fatalf("row=%+v", r)
	}
	// note 空串→保留原 note
	r, err = s.SetAutotradeAllowRuleEnabled(ctx, id, true, "")
	if err != nil {
		t.Fatalf("set2: %v", err)
	}
	if !r.Enabled || r.Note != "d444 flip" {
		t.Fatalf("row2=%+v want note kept", r)
	}
	// 不存在 id → error
	if _, err := s.SetAutotradeAllowRuleEnabled(ctx, 99999999, false, ""); err == nil {
		t.Fatal("expect error for missing id")
	}
	// 全量列表含 disabled 行（含 disabled+simulated 不漏）
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated, note)
		VALUES ('canonical','D444TEST',false,true,'disabled-sim')`); err != nil {
		t.Fatalf("seed2: %v", err)
	}
	all, err := s.ListAutotradeAllowRulesAll(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var sawVenue, sawCanon bool
	for _, r := range all {
		if r.Value == "d444test:mt5" && r.Enabled {
			sawVenue = true
		}
		if r.Value == "D444TEST" && !r.Enabled && r.Simulated {
			sawCanon = true
		}
	}
	if !sawVenue || !sawCanon {
		t.Fatalf("list missing rows: venue=%v canon=%v", sawVenue, sawCanon)
	}
}
