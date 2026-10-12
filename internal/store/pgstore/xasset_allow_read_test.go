package pgstore

// T1-T3：白名单表化——LoadXAssetAllowCombos environment 过滤（simulated 对齐
// exchange_configs 习惯：true=仅 demo / false=仅 real / NULL=两域通用）+enabled 过滤+
// migration 0105 播种四行逐字对齐 .env:27。
import (
	"context"
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
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell, max_long_qty, max_short_qty, max_gross_usd, enabled, simulated, note) VALUES
		('A','B','va','perp','vb','mt5',1,1,100,true,true,'demo only'),
		('A','C','va','perp','vb','mt5',1,1,100,true,false,'real only'),
		('A','D','va','perp','vb','mt5',1,1,100,true,NULL,'both'),
		('A','E','va','perp','vb','mt5',1,1,100,false,true,'disabled')`); err != nil {
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
	// 0105 种子值为旧复合域（venue_buy='gate:perp'），0142 键域宪法后重放
	// 必被裸域 CHECK 拒绝——重放失败本身即契约生效的证据；种子域校验改在 0142 干跑。
	if _, err := s.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, migrations.XassetAllowCombosSQL); err == nil {
		t.Fatal("0105 复合域种子在新 schema 下应被 CHECK 拒绝（宪法生效证据）")
	}
}
