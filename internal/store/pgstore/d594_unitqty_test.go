package pgstore

import (
	"context"
	"math"
	"testing"

	"arb/internal/testutil"
)

// TestD594_UnitScales migration 0161 锚 + UnitQty 语义（CS×qty_scale）+ 敏感集 +
// fail-closed + 别名落库。铜行定标值由 S5 生产库直接取证（测试库无生产种子行，
// venue 维度行自带 FK → 本测自造 venue+映射行）。
func TestD594_UnitScales(t *testing.T) {
	ctx := context.Background()
	s, err := New(ctx, testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer s.Close()
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	var ok bool
	if err := s.pool.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM schema_migrations WHERE version=161)`).Scan(&ok); err != nil {
		t.Fatalf("schema_migrations 查询: %v", err)
	}
	if !ok {
		t.Fatalf("schema_migrations 无 version=161（embed/migrate 双注册缺一）")
	}

	// 自造 venue + 铜三尺度映射行（FK 要求 venue 先落 broker_accounts）。
	for _, v := range []string{"t594a", "t594b", "t594c", "t594d"} {
		if _, err := s.pool.Exec(ctx,
			`INSERT INTO broker_accounts (venue, host, port, password, venue_type)
			 VALUES ($1, '127.0.0.1', 443, 'x', 'mt5')
			 ON CONFLICT (venue) DO NOTHING`, v); err != nil {
			t.Fatalf("seed venue %s: %v", v, err)
		}
	}
	defer s.pool.Exec(ctx, `DELETE FROM broker_accounts WHERE venue LIKE 't594%'`)
	seed := []struct {
		venue, sym, canon string
		cs, ps, qs        float64
	}{
		{"t594a", "COPPER", "XCUUSD", 1, 1.0 / 2204.62, 2204.62},  // $/ton × 1 ton/lot
		{"t594b", "COPPER", "XCUUSD", 25, 1.0 / 2204.62, 2204.62}, // $/ton × 25 ton/lot
		{"t594c", "Copper", "XCUUSD", 2000, 1.0, 1.0},             // $/lb × 2000 lb/lot
		{"t594d", "COPPER", "XCUUSD", 250, 0.01, 1.0},             // ¢/lb × 250 lb/lot
		{"t594a", "EURUSDx", "EURUSD", 100000, 1.0, 1.0},          // 非敏感基线
	}
	for _, r := range seed {
		if _, err := s.pool.Exec(ctx, `
			INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, contract_size, price_scale, qty_scale)
			VALUES ($1, $2, $3, $4, $5, $6)
			ON CONFLICT (venue, broker_symbol) DO UPDATE SET
				canonical=EXCLUDED.canonical, contract_size=EXCLUDED.contract_size,
				price_scale=EXCLUDED.price_scale, qty_scale=EXCLUDED.qty_scale`,
			r.venue, r.sym, r.canon, r.cs, r.ps, r.qs); err != nil {
			t.Fatalf("seed map %s %s: %v", r.venue, r.sym, err)
		}
	}
	defer s.pool.Exec(ctx, `DELETE FROM broker_symbol_map WHERE venue LIKE 't594%'`)

	// 敏感集含 XCUUSD（存在非 1 标定行），不含 EURUSD。
	sens, err := s.ListUnitSensitiveCanonicals(ctx)
	if err != nil {
		t.Fatalf("ListUnitSensitiveCanonicals: %v", err)
	}
	if !sens["XCUUSD"] || sens["EURUSD"] {
		t.Errorf("sensitive set wrong: %v", sens)
	}

	// UnitQtyByBrokerSymbol：canonical lb/手。
	cases := []struct {
		venueKey, sym string
		want          float64
	}{
		{"t594a:mt5", "COPPER", 2204.62},
		{"t594b:mt5", "COPPER", 25 * 2204.62},
		{"t594c:mt5", "Copper", 2000},
		{"t594d:mt5", "COPPER", 250},
		{"t594a:mt5", "EURUSDx", 100000},
	}
	for _, c := range cases {
		u, ok, err := s.UnitQtyByBrokerSymbol(ctx, c.venueKey, c.sym)
		if err != nil || !ok {
			t.Fatalf("UnitQty %s %s: ok=%v err=%v", c.venueKey, c.sym, ok, err)
		}
		if math.Abs(u-c.want) > 0.01 {
			t.Errorf("UnitQty %s %s = %.2f, want %.2f", c.venueKey, c.sym, u, c.want)
		}
	}
	if u, ok, _ := s.UnitQtyByCanonical(ctx, "t594a:mt5", "XCUUSD"); !ok || math.Abs(u-2204.62) > 0.01 {
		t.Errorf("UnitQtyByCanonical=%v ok=%v", u, ok)
	}

	// fail-closed：敏感 canonical 的未标定行 → ok=false；非敏感未标定行 → cs×1。
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, contract_size, price_scale, qty_scale)
		VALUES ('t594a', 'COPPERx', 'XCUUSD', 5, NULL, NULL)
		ON CONFLICT (venue, broker_symbol) DO UPDATE SET price_scale=NULL, qty_scale=NULL`); err != nil {
		t.Fatalf("seed unrated sensitive row: %v", err)
	}
	if _, ok, _ := s.UnitQtyByBrokerSymbol(ctx, "t594a:mt5", "COPPERx"); ok {
		t.Error("unrated sensitive row must fail closed")
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, contract_size, price_scale, qty_scale)
		VALUES ('t594b', 'EURUSDx', 'EURUSD', 100000, NULL, NULL)
		ON CONFLICT (venue, broker_symbol) DO UPDATE SET price_scale=NULL, qty_scale=NULL`); err != nil {
		t.Fatalf("seed unrated fx row: %v", err)
	}
	if u, ok, _ := s.UnitQtyByBrokerSymbol(ctx, "t594b:mt5", "EURUSDx"); !ok || u != 100000 {
		t.Errorf("unrated non-sensitive row: u=%v ok=%v, want 100000/true", u, ok)
	}

	// 别名 XCU→COPPER 落库。
	var canon string
	if err := s.pool.QueryRow(ctx,
		`SELECT canonical FROM fin_symbol_aliases WHERE alias='XCU'`).Scan(&canon); err != nil || canon != "COPPER" {
		t.Errorf("alias XCU→COPPER missing: canon=%q err=%v", canon, err)
	}
}
