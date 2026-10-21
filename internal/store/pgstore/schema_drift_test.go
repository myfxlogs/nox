// schema_drift_test.go —— 约束漂移侦测锚：链上终态 vs 活库对拍。
// ① 干净库零漂移（含 1066 后 cg_venue_type_ab_chk 终态断言——本票验收锚）
// ② 篡改检出（DROP 后报缺席、收窄枚举报定义不符）
// ③ 规范化单测（IN→ANY、::text、空白折叠——写法差异不误报）
package pgstore

import (
	"context"
	"strings"
	"testing"
)

// TestSchemaCheckDrift_Clean 全量迁移后的测试库对拍=零漂移（本票机检验收锚：
// cg venue_type 终态含 'futures' 由期望集断言面覆盖——若 1066 缺位，
// 活库旧枚举与链上终态不符立即报漂）。
func TestSchemaCheckDrift_Clean(t *testing.T) {
	s := testStore(t)
	drift, err := s.SchemaCheckDrift(context.Background())
	if err != nil {
		t.Fatalf("drift check: %v", err)
	}
	if len(drift) != 0 {
		var b strings.Builder
		for _, d := range drift {
			b.WriteString("\n  " + d.String())
		}
		t.Fatalf("干净库应零漂移，实报 %d 处:%s", len(drift), b.String())
	}
}

// TestSchemaCheckDrift_DetectsTamper 手工收窄枚举=漂移源事故复现：
// 活约束回退到无 'futures' 旧定义 → 报「缺席或被改写」；DROP 不重建 → 同面报。
func TestSchemaCheckDrift_DetectsTamper(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	defer func() {
		// 恢复链上终态（共享库纪律：改完必须还原）。
		s.pool.Exec(ctx, `ALTER TABLE carry_graduations DROP CONSTRAINT IF EXISTS cg_venue_type_ab_chk`)
		s.pool.Exec(ctx, `ALTER TABLE carry_graduations ADD CONSTRAINT cg_venue_type_ab_chk
			CHECK (((venue_type_a = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text, 'futures'::text]))
				AND (venue_type_b = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text, 'futures'::text]))))`)
	}()
	if _, err := s.pool.Exec(ctx, `ALTER TABLE carry_graduations DROP CONSTRAINT cg_venue_type_ab_chk`); err != nil {
		t.Fatalf("drop: %v", err)
	}
	if _, err := s.pool.Exec(ctx, `ALTER TABLE carry_graduations ADD CONSTRAINT cg_venue_type_ab_chk
		CHECK (((venue_type_a = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text]))
			AND (venue_type_b = ANY (ARRAY[''::text, 'spot'::text, 'perp'::text]))))`); err != nil {
		t.Fatalf("re-add stale: %v", err)
	}
	drift, err := s.SchemaCheckDrift(ctx)
	if err != nil {
		t.Fatalf("drift: %v", err)
	}
	found := false
	for _, d := range drift {
		if d.Name == "cg_venue_type_ab_chk" && strings.Contains(d.Detail, "或被改写") {
			found = true
		}
	}
	if !found {
		t.Fatalf("收窄 'futures' 未检出: %+v", drift)
	}
	// DROP 不重建 → 缺席面。
	if _, err := s.pool.Exec(ctx, `ALTER TABLE carry_graduations DROP CONSTRAINT cg_venue_type_ab_chk`); err != nil {
		t.Fatalf("drop2: %v", err)
	}
	drift, err = s.SchemaCheckDrift(ctx)
	if err != nil {
		t.Fatalf("drift2: %v", err)
	}
	found = false
	for _, d := range drift {
		if d.Name == "cg_venue_type_ab_chk" && strings.Contains(d.Detail, "或被改写") {
			found = true
		}
	}
	if !found {
		t.Fatalf("DROP 缺席未检出: %+v", drift)
	}
}

func TestNormalizeCheckDef(t *testing.T) {
	cases := []struct{ in, want string }{
		{`((status = ANY (ARRAY['pending'::text, 'approved'::text])))`,
			`status = ANY ARRAY['pending','approved']`},
		{`(status IN ('pending','approved','stale'))`,
			`status = ANY ARRAY['pending','approved','stale']`},
		{"(( venue_type = ANY (  ARRAY['perp'::text] ) ))", `venue_type = 'perp'`},
		{`(venue NOT LIKE '%:%')`, `venue !~~ '%:%'`},
		{`((a <> '') AND (b !~~ '%:%'))`, `a <> '' AND b !~~ '%:%'`},
	}
	for _, c := range cases {
		if got := normalizeCheckDef(c.in); got != c.want {
			t.Errorf("normalize(%q)=%q want %q", c.in, got, c.want)
		}
	}
}

// TestExpectedCheckDefs_Chain 链上终态锚：1066 后 cg chk 期望定义含 'futures'；
// status_check 期望=1034 末次定义（unqualified/stale 在集——末次定义胜出语义）。
func TestExpectedCheckDefs_Chain(t *testing.T) {
	defs, _ := expectedCheckDefs()
	cg, ok := defs["carry_graduations"]["cg_venue_type_ab_chk"]
	if !ok {
		t.Fatal("期望集缺 carry_graduations.cg_venue_type_ab_chk")
	}
	if !strings.Contains(cg, "'futures'") {
		t.Fatalf("cg chk 期望定义缺 'futures': %s", cg)
	}
	st, ok := defs["carry_graduations"]["carry_graduations_status_check"]
	if !ok || !strings.Contains(st, "'unqualified'") || !strings.Contains(st, "'stale'") {
		t.Fatalf("status_check 末次定义胜出失败: %q", st)
	}
}

// TestSchemaCheckDrift_ExemptRuntimeLegacy 运行时遗留表豁免锚（ticks_old 白名单修）：
// ①豁免表 live-only 不报告；②非豁免 live-only 表仍报「无声明」——禁宽掩码不松口。
func TestSchemaCheckDrift_ExemptRuntimeLegacy(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	defer func() {
		s.pool.Exec(ctx, `DROP TABLE IF EXISTS ticks_old`)
		s.pool.Exec(ctx, `DROP TABLE IF EXISTS zdrift_noexempt`)
	}()
	if _, err := s.pool.Exec(ctx, `CREATE TABLE ticks_old (id int CHECK (id > 0));
		CREATE TABLE zdrift_noexempt (id int CHECK (id > 0))`); err != nil {
		t.Fatal(err)
	}
	drift, err := s.SchemaCheckDrift(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var exemptHit, noexemptHit bool
	for _, d := range drift {
		if d.Table == "ticks_old" {
			exemptHit = true
		}
		if d.Table == "zdrift_noexempt" {
			noexemptHit = true
		}
	}
	if exemptHit {
		t.Fatal("ticks_old 豁免表被误报漂移")
	}
	if !noexemptHit {
		t.Fatal("非豁免 live-only 表未报——豁免面过宽（宽掩码回归）")
	}
}
