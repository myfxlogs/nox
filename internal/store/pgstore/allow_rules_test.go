package pgstore

// D-443 T1：autotrade_allow_rules 加载器——kind 分流两 map + enabled/simulated 过滤 +
// 空集非 nil（venue 空=全拒 / canonical 空=全放行 D-122）。
import (
	"context"
	"testing"

	"arb/migrations"
)

func TestD443_LoadAutotradeAllowRules(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_allow_rules`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated) VALUES
		('venue','277842155:mt5',true,false),
		('venue','okx:perp',true,false),
		('venue','dead:mt5',false,false),       -- disabled 过滤
		('venue','demo-only:mt5',true,true),    -- simulated 分流
		('canonical','BTCUSDT',true,false),
		('canonical','ETHUSDT',false,false)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	venues, canonicals, err := s.LoadAutotradeAllowRules(ctx, false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(venues) != 2 || !venues["277842155:mt5"] || !venues["okx:perp"] {
		t.Errorf("venues got %v, want {277842155:mt5, okx:perp}（disabled/simulated 行剔除）", venues)
	}
	if len(canonicals) != 1 || !canonicals["BTCUSDT"] {
		t.Errorf("canonicals got %v, want {BTCUSDT}", canonicals)
	}
	dv, dc, err := s.LoadAutotradeAllowRules(ctx, true)
	if err != nil {
		t.Fatalf("load demo: %v", err)
	}
	if len(dv) != 1 || !dv["demo-only:mt5"] || len(dc) != 0 {
		t.Errorf("simulated=true got venues=%v canonicals=%v, want {demo-only:mt5}+空", dv, dc)
	}
}

func TestD443_LoadAutotradeAllowRulesEmpty(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_allow_rules`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	venues, canonicals, err := s.LoadAutotradeAllowRules(ctx, false)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if venues == nil || canonicals == nil {
		t.Fatal("空集应返回非 nil map（nil map 读侧同语义但契约钉死防漂移）")
	}
	if len(venues) != 0 || len(canonicals) != 0 {
		t.Errorf("空表应返回空集, got venues=%v canonicals=%v", venues, canonicals)
	}
}

func TestD443_MigrationSeed(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 同包其他测试 TRUNCATE 会抹 seed——幂等重放 0134 SQL（ON CONFLICT DO NOTHING）
	// 再断言 seed 行数（锚=migration 文本本身非测试内手抄清单）。
	if _, err := s.Pool().Exec(ctx, migrations.AutotradeAllowRulesSQL); err != nil {
		t.Fatalf("replay 0134: %v", err)
	}
	var n int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*) FROM autotrade_allow_rules WHERE kind='venue' AND enabled AND NOT simulated`).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 10 {
		t.Errorf("seed venue 行=%d, want 10（env 全镜像）", n)
	}
	var v bool
	if err := s.Pool().QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM autotrade_allow_rules WHERE kind='canonical')`).Scan(&v); err != nil {
		t.Fatalf("canonical exists: %v", err)
	}
	if v {
		t.Error("seed canonical 应零行（空集=全放行 D-122 语义保真）")
	}
}
