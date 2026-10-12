package main

// D-338 T4/T5：白名单表加载装配语义——loader 错误→fatal（fail-closed）、零行→合法空态、
// env 残值仅告警不消费。
import (
	"context"
	"testing"

	"nox/internal/platform/config"
	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
	"nox/migrations"
)

func testAccumStoreMain(t *testing.T) *pgstore.Store {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestD338_XassetAllowFromTableZeroRows(t *testing.T) {
	s := testAccumStoreMain(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// D-367+D-010：跨 run 残留表形态自愈（LoadErrorFatal cleanup 重建为 D-338 时代形态）——
	// 幂等补列，保证含 notional_usd/margin_usd 的 SELECT 不因残留形态炸加载。
	if _, err := s.Pool().Exec(ctx, `ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS notional_usd NUMERIC;
		ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS margin_usd NUMERIC`); err != nil {
		t.Fatalf("self-heal column: %v", err)
	}
	got := xassetAllowFromTable(ctx, s, "demo")
	if got == nil || len(got) != 0 {
		t.Errorf("零行应返回空 slice（合法态=全拒）, got %v", got)
	}
}

func TestD338_XassetAllowFromTableRows(t *testing.T) {
	s := testAccumStoreMain(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// D-367+D-010：跨 run 残留表形态自愈（LoadErrorFatal cleanup 重建为 D-338 时代形态）——
	// 幂等补列，保证含 notional_usd/margin_usd 的 SELECT 不因残留形态炸加载。
	if _, err := s.Pool().Exec(ctx, `ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS notional_usd NUMERIC;
		ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS margin_usd NUMERIC`); err != nil {
		t.Fatalf("self-heal column: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell, max_long_qty, max_short_qty, max_gross_usd, simulated, note)
		VALUES ('XAUUSD','XAUUSDT','a','mt5','b','perp',0.01,5,10000,true,'seed')`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got := xassetAllowFromTable(ctx, s, "demo")
	if len(got) != 1 || got[0].CanonicalSell != "XAUUSDT" || got[0].MaxShortQty != 5 {
		t.Fatalf("rows=%+v, want 1 行 XAUUSDT short=5", got)
	}
}

// TestD338_LoadErrorFatal T4：loader 错误 → startup fatal（fail-closed，映射不继续）。
// seam stub：xassetAllowFatal 捕获调用（真 log.Fatalf 会杀测试进程）。
// 红=摘 fatal 变异（改 Printf 吞错）→ fatal 未被调、函数继续返回 → 断言红。
func TestD338_LoadErrorFatal(t *testing.T) {
	s := testAccumStoreMain(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DROP TABLE xasset_allow_combos`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	t.Cleanup(func() {
		s.Pool().Exec(ctx, migrations.XassetAllowCombosSQL)
		// D-367：D-338 时代建表 SQL 无 notional_usd——补列到最新形态，防后续测试
		// （SELECT 含该列）加载失败（log.Fatalf 会杀整个测试二进制）。
		s.Pool().Exec(ctx, `ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS notional_usd NUMERIC;
			ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS margin_usd NUMERIC`)
		// D-496 F4：重建表须补齐 0142 键域形态（venue_type 拆列+venue_key 生成列+CHECK/唯一键），
		// 否则残留旧形态使后续测试的 LoadXAssetAllowCombos（读 venue_key_*）误炸。
		if _, err := s.Pool().Exec(ctx, `
			ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS venue_type_buy text,
				ADD COLUMN IF NOT EXISTS venue_type_sell text;
			UPDATE xasset_allow_combos SET venue_type_buy = split_part(venue_buy, ':', 2) WHERE position(':' IN venue_buy) > 0;
			UPDATE xasset_allow_combos SET venue_type_sell = split_part(venue_sell, ':', 2) WHERE position(':' IN venue_sell) > 0;
			UPDATE xasset_allow_combos SET venue_buy = split_part(venue_buy, ':', 1), venue_sell = split_part(venue_sell, ':', 1);
			UPDATE xasset_allow_combos SET venue_type_buy = '' WHERE venue_type_buy IS NULL;
			UPDATE xasset_allow_combos SET venue_type_sell = '' WHERE venue_type_sell IS NULL;
			ALTER TABLE xasset_allow_combos ALTER COLUMN venue_type_buy SET NOT NULL, ALTER COLUMN venue_type_buy SET DEFAULT '';
			ALTER TABLE xasset_allow_combos ALTER COLUMN venue_type_sell SET NOT NULL, ALTER COLUMN venue_type_sell SET DEFAULT '';
			ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS venue_key_buy text
				GENERATED ALWAYS AS (CASE WHEN venue_type_buy = '' THEN venue_buy ELSE venue_buy || ':' || venue_type_buy END) STORED,
				ADD COLUMN IF NOT EXISTS venue_key_sell text
				GENERATED ALWAYS AS (CASE WHEN venue_type_sell = '' THEN venue_sell ELSE venue_sell || ':' || venue_type_sell END) STORED;
			ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_canonical_buy_canonical_sell_venue_buy__key;
			ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_allow_combos_uniq;
			ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_allow_combos_uniq UNIQUE
				(canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell);
			ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_venue_bare_chk;
			ALTER TABLE xasset_allow_combos DROP CONSTRAINT IF EXISTS xasset_venue_type_chk;
			ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_venue_bare_chk
				CHECK (venue_buy NOT LIKE '%:%' AND venue_sell NOT LIKE '%:%');
			ALTER TABLE xasset_allow_combos ADD CONSTRAINT xasset_venue_type_chk
				CHECK (venue_type_buy IN ('mt4','mt5','perp','spot','funding','earn','option','unified','')
				   AND venue_type_sell IN ('mt4','mt5','perp','spot','funding','earn','option','unified',''))`); err != nil {
			t.Logf("cleanup 0142 形态重建失败（后续测试可能误炸）: %v", err)
		}
	})
	fatalMsg := ""
	orig := xassetAllowFatal
	xassetAllowFatal = func(format string, args ...any) {
		fatalMsg = format
		panic("fatal-called") // 阻断后续映射（镜像进程退出语义）
	}
	defer func() { xassetAllowFatal = orig }()
	defer func() {
		if r := recover(); r == nil {
			t.Error("loader 错误应触发 fatal 阻断（红=摘 fatal 吞错继续执行）")
		}
		if fatalMsg == "" {
			t.Error("fatal 未被调用（红=摘 fatal 变异）")
		}
	}()
	xassetAllowFromTable(ctx, s, "demo")
}

// TestD338_WarnXAssetEnvDeprecated T5：env 残值仅告警不消费（调用无副作用；空值静默；
// 「不进消费路径」由报审 grep 证据：ParseXAssetComboAllow 仅剩 config 构造+exec 测试）。
func TestD338_WarnXAssetEnvDeprecated(t *testing.T) {
	cfg := &config.Config{}
	cfg.XAssetAllowCombosRaw = "XAUUSD|XAUUSDT|a:mt5|b:perp|1|1|100"
	WarnXAssetEnvDeprecated(cfg) // 残值 → warn（log 锚，跳过记边界）
	cfg.XAssetAllowCombosRaw = ""
	WarnXAssetEnvDeprecated(cfg) // 空值静默
}

// TestD367_NotionalNonPositiveDropped D-367 S5 T3：行值 ≤0 → 加载期剔除+boot warn
// （fail-closed 坏配置行不授权任何交易；行不进 spec 集）。
// 红=T3 旧行为（无列无剔除）下不存在——列收编后天然可测。
func TestD367_NotionalNonPositiveDropped(t *testing.T) {
	s := testAccumStoreMain(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE xasset_allow_combos`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// D-367：跨 run 残留表形态自愈（LoadErrorFatal cleanup 重建为 D-338 时代形态）——
	// 幂等补列，保证含 notional_usd 的 SELECT 不因残留形态炸加载。
	if _, err := s.Pool().Exec(ctx, `ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS notional_usd NUMERIC`); err != nil {
		t.Fatalf("self-heal column: %v", err)
	}
	// D-338 LoadErrorFatal 的 cleanup 会用 D-338 时代建表 SQL 重建本表（无新列）——
	// 本锚开头幂等自愈 notional/margin 列（0116 同款+D-010 margin_usd），不依赖测试顺序。
	if _, err := s.Pool().Exec(ctx, `ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS notional_usd NUMERIC;
		ALTER TABLE xasset_allow_combos ADD COLUMN IF NOT EXISTS margin_usd NUMERIC`); err != nil {
		t.Fatalf("self-heal column: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO xasset_allow_combos (canonical_buy, canonical_sell, venue_buy, venue_type_buy, venue_sell, venue_type_sell, max_long_qty, max_short_qty, max_gross_usd, notional_usd, simulated)
		VALUES ('XAUUSD','XAUUSDT','a','mt5','b','perp',0.01,5,10000,-5,true),
		       ('PAXGUSDT','PAXGUSDT','c','perp','d','perp',0.01,5,10000,NULL,true),
		       ('NAS100','NAS100','e','mt5','f','mt5',0.05,5,100000,5000,true)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	orig := xassetAllowFatal
	xassetAllowFatal = func(string, ...interface{}) {}
	defer func() { xassetAllowFatal = orig }()
	got := xassetAllowFromTable(ctx, s, "demo")
	if len(got) != 2 {
		t.Fatalf("rows = %d, want 2（≤0 行剔除、NULL/正数保留）: %+v", len(got), got)
	}
	for _, spec := range got {
		if spec.NotionalUSD != nil && *spec.NotionalUSD <= 0 {
			t.Errorf("剔除失效：≤0 行仍进 spec 集 (%+v)", spec)
		}
	}
	// NAS100 行：组合级 5000 生效（T1 表来源端到端）
	var nas *exec.XAssetComboAllowSpec
	for i := range got {
		if got[i].CanonicalBuy == "NAS100" {
			nas = &got[i]
		}
	}
	if nas == nil || nas.NotionalUSD == nil || *nas.NotionalUSD != 5000 {
		t.Errorf("NAS100 notional 透传失败: %+v", nas)
	}
}

// TestD443_AllowRulesFromTable T3：live 空 venue 集 → fatal（fail-closed 装配门）；
// demo 空集 → warn+空 map 合法态；行分流两 map。
func TestD443_AllowRulesFromTable(t *testing.T) {
	s := testAccumStoreMain(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_allow_rules`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// demo 空集 → 非 fatal，返回空 map
	v, c := allowRulesFromTable(ctx, s, "demo")
	if v == nil || c == nil || len(v) != 0 || len(c) != 0 {
		t.Fatalf("demo 空集应返回空 map 非 nil, got %v %v", v, c)
	}
	// live 空 venue 集 → fatal（stub panic 镜像进程退出）
	fatalMsg := ""
	orig := allowRulesFatal
	allowRulesFatal = func(format string, args ...any) { fatalMsg = format; panic("fatal-called") }
	defer func() { allowRulesFatal = orig }()
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("live 空 venue 集应触发 fatal（红=摘 fatal 变异）")
			}
			if fatalMsg == "" {
				t.Error("fatal 未被调用")
			}
		}()
		allowRulesFromTable(ctx, s, "live")
	}()
	// live 有 venue 行 → 放行不 fatal
	if _, err := s.Pool().Exec(ctx, `INSERT INTO autotrade_allow_rules (kind, value, enabled, simulated) VALUES ('venue','a:mt5',true,false)`); err != nil {
		t.Fatalf("seed: %v", err)
	}
	v, _ = allowRulesFromTable(ctx, s, "live")
	if !v["a:mt5"] {
		t.Errorf("live 行应加载, got %v", v)
	}
}
