// brokers_symbolmap_d383_test.go —— D-383 A3/A4/A5/A8（pgstore 面）：
// ResolvePrimarySymbol primary-only/隐式/歧义 fail-closed + migration 0122 幂等 +
// 部分唯一索引冲突实证 + 空 canonical 不受约束。
package pgstore

import (
	"context"
	"strings"
	"testing"

	"arb/internal/testutil"
)

func d383MapStore(t *testing.T) *Store {
	t.Helper()
	s, err := New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	t.Cleanup(s.Close)
	if _, err := s.pool.Exec(context.Background(), `TRUNCATE broker_symbol_map`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return s
}

func d383MapSeed(t *testing.T, s *Store, venue, sym, canon string, primary bool) {
	seedBrokerVenue(t, s, venue) // D-496：FK 父行
	t.Helper()
	if _, err := s.pool.Exec(context.Background(),
		`INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, is_primary) VALUES ($1,$2,$3,$4)`,
		venue, sym, canon, primary); err != nil {
		t.Fatalf("seed %s.%s: %v", venue, sym, err)
	}
}

// TestD383A3_ResolvePrimaryOnly A3：双行组 resolver 返回唯一 primary（XAUUSDm），
// 不取 247m 类变体；单行组隐式 primary 照常解析（A8）。
func TestD383A3_ResolvePrimaryOnly(t *testing.T) {
	s := d383MapStore(t)
	ctx := context.Background()
	d383MapSeed(t, s, "v", "XAUUSDm", "XAUUSD", true)
	d383MapSeed(t, s, "v", "XAUUSD247m", "XAUUSD", false)
	d383MapSeed(t, s, "v2", "EURUSDm", "EURUSD", false) // 新行默认 false，单行组隐式

	sym, err := s.ResolvePrimarySymbol(ctx, "v", "XAUUSD")
	if err != nil || sym != "XAUUSDm" {
		t.Fatalf("resolve = %s/%v, want XAUUSDm/nil（红=resolver 取变体或非唯一）", sym, err)
	}
	sym, err = s.ResolvePrimarySymbol(ctx, "v2", "EURUSD")
	if err != nil || sym != "EURUSDm" {
		t.Fatalf("隐式单行 resolve = %s/%v, want EURUSDm/nil（A8 不误伤）", sym, err)
	}
}

// TestD383A4_ResolveAmbiguousFailClosed A4 解析面：多行组零 primary → 歧义态错误
// （fail-closed 拒单）；未映射/空 canonical → not mapped。
func TestD383A4_ResolveAmbiguousFailClosed(t *testing.T) {
	s := d383MapStore(t)
	ctx := context.Background()
	d383MapSeed(t, s, "v", "A", "CAN", false)
	d383MapSeed(t, s, "v", "B", "CAN", false)

	_, err := s.ResolvePrimarySymbol(ctx, "v", "CAN")
	if err == nil || !strings.Contains(err.Error(), "symbol resolution ambiguous (v, CAN)") {
		t.Fatalf("err = %v, want 含 symbol resolution ambiguous (v, CAN)（红=歧义态未 fail-closed）", err)
	}
	if _, err := s.ResolvePrimarySymbol(ctx, "v9", "NOPE"); err == nil ||
		!strings.Contains(err.Error(), "symbol not mapped") {
		t.Fatalf("未映射 err = %v, want symbol not mapped", err)
	}
	if _, err := s.ResolvePrimarySymbol(ctx, "v", ""); err == nil {
		t.Fatal("空 canonical 应 not mapped 错误")
	}
}

// TestD383A5_MigrationIdempotentAndUniqueIndex A5：migration 幂等再跑无错；
// 部分唯一索引冲突实证（同 (venue,canonical) 第二个 primary 被拒）；空 canonical 行
// 双 primary 不受索引约束（审计 R4 防御域）。
func TestD383A5_MigrationIdempotentAndUniqueIndex(t *testing.T) {
	s := d383MapStore(t)
	ctx := context.Background()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate 幂等再跑失败（0122 含种子/index 须可重放）: %v", err)
	}
	d383MapSeed(t, s, "v", "A", "CAN", true)
	if _, err := s.pool.Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password) VALUES ('v','mt5','h',443,'p') ON CONFLICT (venue) DO NOTHING; INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, is_primary) VALUES ('v','B','CAN',true)`); err == nil {
		t.Fatal("同 (venue,canonical) 第二个 primary 应被部分唯一索引拒绝（红=索引缺失/域错）")
	}
	// 空 canonical 行：双 primary 放行（索引 WHERE canonical <> ''）
	for _, sym := range []string{"E1", "E2"} {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, is_primary) VALUES ('v',$1,'',true)`, sym); err != nil {
			t.Fatalf("空 canonical 行 %s 不应受索引约束: %v", sym, err)
		}
	}
}

// TestD383A8_SeededGroupSurvivesVariant A8 后半：固化 primary 的单行组被 rule 回填
// variant 后（组变多行）——primary 仍指定行，resolver/订阅语义不断流。
func TestD383A8_SeededGroupSurvivesVariant(t *testing.T) {
	s := d383MapStore(t)
	ctx := context.Background()
	d383MapSeed(t, s, "v", "XAUUSDm", "XAUUSD", true) // migration 固化态
	// rule 回填复活（247m 场景，is_primary 默认 false）
	d383MapSeed(t, s, "v", "XAUUSD247m", "XAUUSD", false)

	sym, err := s.ResolvePrimarySymbol(ctx, "v", "XAUUSD")
	if err != nil || sym != "XAUUSDm" {
		t.Fatalf("resolve = %s/%v, want XAUUSDm（canonical 不断流）", sym, err)
	}
	mappings, err := s.ListSymbolMappings(ctx, "v")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(mappings) != 2 {
		t.Fatalf("mappings = %d 行, want 2", len(mappings))
	}
	bySym := map[string]bool{}
	for _, m := range mappings {
		bySym[m.BrokerSymbol] = m.IsPrimary
	}
	if !bySym["XAUUSDm"] || bySym["XAUUSD247m"] {
		t.Fatalf("is_primary 透传错: %+v（XAUUSDm 恒 true、variant 恒 false）", mappings)
	}
}
