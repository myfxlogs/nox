// migration_1067_test.go —— D-648 venue_type 'futures' 全谱 CHECK 修：
// ① 全谱表 pg_get_constraintdef 含 'futures'（9 值域 8 表+ticks 族 5 值域）；
// ② 值域对拍——xasset_allow_combos/opportunities 存量值⊆新域（超集扩张）；
// ③ broker_accounts MT 域不动（mt4|mt5——futures 不入错域）。
package pgstore

import (
	"context"
	"strings"
	"testing"
)

func chkDef(t *testing.T, s *Store, table, name string) string {
	t.Helper()
	var def string
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname=$1 AND conrelid=$2::regclass`,
		name, table).Scan(&def); err != nil {
		t.Fatalf("%s.%s def: %v", table, name, err)
	}
	return def
}

func TestMigration1067_AllVenueTypeChkHaveFutures(t *testing.T) {
	s := testStore(t)
	cases := [][2]string{
		{"autotrade_logs", "autotrade_logs_venue_type_chk"},
		{"facts", "facts_venue_type_chk"},
		{"income_events", "income_events_venue_type_chk"},
		{"opportunities", "opp_venue_type_chk"},
		{"position_cashflows", "position_cashflows_venue_type_chk"},
		{"position_legs", "position_legs_venue_type_chk"},
		{"position_swap_snapshots", "pss_venue_type_chk"},
		{"xasset_allow_combos", "xasset_venue_type_chk"},
		{"ticks", "ticks_venue_type_chk"},
	}
	for _, c := range cases {
		def := chkDef(t, s, c[0], c[1])
		if !strings.Contains(def, "'futures'") {
			t.Errorf("%s.%s 缺 'futures': %s", c[0], c[1], def)
		}
	}
	// ticks 活动分区同谱（随月动态生——枚举全覆盖）。
	rows, err := s.Pool().Query(context.Background(), `
		SELECT cl.relname, pg_get_constraintdef(c.oid)
		  FROM pg_constraint c
		  JOIN pg_class cl ON cl.oid=c.conrelid
		  JOIN pg_inherits i ON i.inhrelid=cl.oid
		  JOIN pg_class p ON p.oid=i.inhparent
		 WHERE p.relname='ticks' AND c.conname='ticks_venue_type_chk'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var rel, def string
		rows.Scan(&rel, &def)
		if !strings.Contains(def, "'futures'") {
			t.Errorf("分区 %s 缺 'futures': %s", rel, def)
		}
	}
}

func TestMigration1067_DomainSupersetAndBrokerUntouched(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 值域对拍：xasset_allow_combos/opportunities 存量 venue_type* 值⊆新域
	// （超集扩张定理上恒真——实证断言防域外值行拦 ALTER）。
	for _, q := range []string{
		`SELECT count(*) FROM xasset_allow_combos WHERE venue_type_buy NOT IN ('mt4','mt5','perp','spot','funding','earn','option','unified','futures','') OR venue_type_sell NOT IN ('mt4','mt5','perp','spot','funding','earn','option','unified','futures','')`,
		`SELECT count(*) FROM opportunities WHERE venue_type_buy NOT IN ('mt4','mt5','perp','spot','funding','earn','option','unified','futures','') OR venue_type_sell NOT IN ('mt4','mt5','perp','spot','funding','earn','option','unified','futures','')`,
	} {
		var n int
		if err := s.Pool().QueryRow(ctx, q).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 0 {
			t.Fatalf("域外值行 %d: %s", n, q)
		}
	}
	// broker_accounts MT 域语义不动（mt4|mt5——'futures' 不入 MT 通道域）。
	def := chkDef(t, s, "broker_accounts", "broker_accounts_venue_type_chk")
	if strings.Contains(def, "'futures'") {
		t.Fatalf("broker_accounts MT 域被误开: %s", def)
	}
}
