package pgstore

// D-338 T1-T3：白名单表化——LoadXAssetAllowCombos environment 过滤（simulated 对齐
// exchange_configs 习惯：true=仅 demo / false=仅 real / NULL=两域通用）+enabled 过滤+
// migration 0105 播种四行逐字对齐 .env:27。
import (
	"context"
	"strconv"
	"testing"

	"arb/migrations"
)

func TestD338_LoadXAssetAllowCombos(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_sell, max_long_qty, max_short_qty, max_gross_usd, enabled, simulated, note) VALUES
		('A','B','va:perp','vb:mt5',1,1,100,true,true,'demo only'),
		('A','C','va:perp','vb:mt5',1,1,100,true,false,'real only'),
		('A','D','va:perp','vb:mt5',1,1,100,true,NULL,'both'),
		('A','E','va:perp','vb:mt5',1,1,100,false,true,'disabled')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	demo, err := s.LoadXAssetAllowCombos(ctx, "demo")
	if err != nil {
		t.Fatalf("load demo: %v", err)
	}
	if len(demo) != 2 { // B(demo)+D(两域)
		t.Fatalf("demo rows=%d, want 2（红=现无此函数/过滤错）: %+v", len(demo), demo)
	}
	gotSells := map[string]bool{}
	for _, r := range demo {
		gotSells[r.CanonicalSell] = true
	}
	if !gotSells["B"] || !gotSells["D"] || gotSells["C"] {
		t.Errorf("demo 集合错: %v", gotSells)
	}
	real, err := s.LoadXAssetAllowCombos(ctx, "real")
	if err != nil {
		t.Fatalf("load real: %v", err)
	}
	if len(real) != 2 { // C(real)+D(两域)
		t.Fatalf("real rows=%d, want 2", len(real))
	}
	gotSells = map[string]bool{}
	for _, r := range real {
		gotSells[r.CanonicalSell] = true
	}
	if !gotSells["C"] || !gotSells["D"] || gotSells["B"] {
		t.Errorf("real 集合错: %v", gotSells)
	}
	// 未知 environment（既非 demo 非 real）→ 只回两域通用行（simulated=($1='demo')=false→real 分支同款）
	odd, err := s.LoadXAssetAllowCombos(ctx, "staging")
	if err != nil || len(odd) != 2 {
		t.Errorf("staging: rows=%d err=%v, want 2（NULL 通用行）", len(odd), err)
	}
}

func TestD338_MigrationSeed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 0105 幂等（IF NOT EXISTS+ON CONFLICT）：TRUNCATE 后重放 migration 原文（embed 逐字）——
	// 防 T1 串场且验证播种 SQL 文件本身的逐字性（改文件即破锚）。
	if _, err := s.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, migrations.XassetAllowCombosSQL); err != nil {
		t.Fatalf("replay 0105: %v", err)
	}
	rows, err := s.Pool().Query(ctx, `
		SELECT canonical_buy, canonical_sell, venue_buy, venue_sell, max_long_qty, max_short_qty, max_gross_usd, simulated
		FROM xasset_allow_combos ORDER BY id`)
	if err != nil {
		t.Fatalf("query: %v（红=migration 0105 未落）", err)
	}
	defer rows.Close()
	want := [][7]any{
		{"CLUSDT", "WTI", "gate:perp", "277842155:mt5", "2000", "0.05", "2500"},
		{"XAUUSD", "XAUUSDT", "277842155:mt5", "gate:perp", "0.01", "10000", "10000"},
		{"XAUUSD", "XAUUSDT", "277842155:mt5", "binance:perp", "0.01", "5", "10000"},
		{"XAUUSD", "PAXGUSDT", "277842155:mt5", "binance:perp", "0.01", "5", "10000"},
	}
	i := 0
	for rows.Next() {
		var cb, cs, vb, vs string
		var ml, ms, mg float64
		var sim *bool
		if err := rows.Scan(&cb, &cs, &vb, &vs, &ml, &ms, &mg, &sim); err != nil {
			t.Fatalf("scan: %v", err)
		}
		if i >= len(want) {
			t.Fatalf("播种行数超 4")
		}
		w := want[i]
		got := []string{cb, cs, vb, vs}
		exp := []string{w[0].(string), w[1].(string), w[2].(string), w[3].(string)}
		for j := range exp {
			if got[j] != exp[j] {
				t.Errorf("row%d col%d: got %q want %q（.env:27 逐字）", i, j, got[j], exp[j])
			}
		}
		wants := []float64{mustF(t, w[4].(string)), mustF(t, w[5].(string)), mustF(t, w[6].(string))}
		gots := []float64{ml, ms, mg}
		for j := range wants {
			if gots[j] != wants[j] {
				t.Errorf("row%d qty%d: got %v want %v", i, j, gots[j], wants[j])
			}
		}
		if sim == nil || !*sim {
			t.Errorf("row%d simulated want true（四行均 demo seed）", i)
		}
		i++
	}
	if i != 4 {
		t.Errorf("播种行数=%d, want 4", i)
	}
}

func mustF(t *testing.T, s string) float64 {
	t.Helper()
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return f
}
