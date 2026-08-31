package pgstore

import (
	"context"
	"testing"
)

// TestMigration0051_CleanPollution D-176：migration 0051 三条 DELETE 清理 97 行污染 + 保留合法映射。
// 需 ARB_TEST_PG_DSN，未设置时 skip（testStore 门禁）。
func TestMigration0051_CleanPollution(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	// 清空 broker_symbol_map，插入已知污染 + 合法映射
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map RESTART IDENTITY`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// 污染行（应被删除）
	pollution := []struct {
		venue, brokerSymbol, canonical, source string
	}{
		// 第一条：股票代码（含 . 的 rule 行）
		{"v1", "CLNE.NAS", "WTI", "rule"},
		{"v1", "CLF.NYSE", "WTI", "rule"},
		{"v1", "HSIC.NAS", "HK50", "rule"},
		{"v1", "GOLD.NYSE-24", "XAUUSD", "rule"},
		// 第二条：贵金属跨币种（非 USD 计价）
		{"v1", "XAGAUD", "SILVER", "rule"},
		{"v1", "XAUAUD", "XAUUSD", "rule"},
		{"v1", "XAGGBP", "SILVER", "rule"},
		// 第三条：已知错误变体
		{"v1", "GOLDEURO", "XAUUSD", "rule"},
		{"v1", "SILVEREURO", "SILVER", "rule"},
		{"v1", "XAUXAG", "XAUUSD", "rule"},
	}
	for _, p := range pollution {
		if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{
			Venue: p.venue, BrokerSymbol: p.brokerSymbol, Canonical: p.canonical, ContractSize: nil, Source: p.source,
		}); err != nil {
			t.Fatalf("insert pollution %s: %v", p.brokerSymbol, err)
		}
	}
	// 合法行（应保留）
	valid := []struct {
		venue, brokerSymbol, canonical, source string
	}{
		{"v1", "XAGUSD", "SILVER", "rule"},
		{"v1", "XAGUSDm", "SILVER", "rule"},
		{"v1", "SILVER365", "SILVER", "rule"},
		{"v1", "SILVER", "SILVER", "rule"},
		{"v1", "XAUUSD", "XAUUSD", "rule"},
		{"v1", "XAUUSDm", "XAUUSD", "rule"},
		{"v1", "XAUUSD247m", "XAUUSD", "rule"},
		{"v1", "GOLD", "XAUUSD", "rule"},
		{"v1", "GOLD365", "XAUUSD", "rule"},
		{"v1", "WTI", "WTI", "rule"},
		{"v1", "USOIL", "WTI", "rule"},
		{"v1", "HK50", "HK50", "rule"},
		{"v1", "EURUSD", "EURUSD", "manual"}, // 非 rule 行，含 . 也不删（第一条只删 source='rule'）
	}
	for _, v := range valid {
		if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{
			Venue: v.venue, BrokerSymbol: v.brokerSymbol, Canonical: v.canonical, ContractSize: nil, Source: v.source,
		}); err != nil {
			t.Fatalf("insert valid %s: %v", v.brokerSymbol, err)
		}
	}

	// 执行 0051 三条 DELETE（直接跑 SQL，模拟 migration 应用）
	stmts := []string{
		`DELETE FROM broker_symbol_map WHERE source = 'rule' AND broker_symbol ~* '\.'`,
		`DELETE FROM broker_symbol_map WHERE canonical IN ('SILVER', 'XAUUSD', 'XPDUSD', 'XPTUSD', 'XCUUSD') AND broker_symbol !~* '^(xag|xau|xpd|xpt|xcu)usd' AND broker_symbol !~* '^(silver|gold|palladium|platinum|copper)'`,
		`DELETE FROM broker_symbol_map WHERE canonical IN ('SILVER', 'XAUUSD') AND broker_symbol ~* '^(goldeuro|goldgr|goldoz|silvereuro|xauxag|xagcad|xagchf|xagnzd)$'`,
	}
	for i, stmt := range stmts {
		if _, err := s.pool.Exec(ctx, stmt); err != nil {
			t.Fatalf("DELETE %d: %v", i+1, err)
		}
	}

	// 验证：污染行全删
	maps, err := s.ListSymbolMappings(ctx, "v1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	got := map[string]string{} // brokerSymbol → canonical
	for _, m := range maps {
		got[m.BrokerSymbol] = m.Canonical
	}
	for _, p := range pollution {
		if _, ok := got[p.brokerSymbol]; ok {
			t.Errorf("pollution %s should be deleted, still exists → %s", p.brokerSymbol, got[p.brokerSymbol])
		}
	}
	// 验证：合法行全保留
	for _, v := range valid {
		canon, ok := got[v.brokerSymbol]
		if !ok {
			t.Errorf("valid %s should be retained, deleted", v.brokerSymbol)
			continue
		}
		if canon != v.canonical {
			t.Errorf("valid %s: canonical %s, want %s", v.brokerSymbol, canon, v.canonical)
		}
	}
	// C-1 验证：SILVER365/GOLD365 仍存在（未被误删）
	if _, ok := got["SILVER365"]; !ok {
		t.Error("C-1: SILVER365 should be retained (no $ anchor in keep condition)")
	}
	if _, ok := got["GOLD365"]; !ok {
		t.Error("C-1: GOLD365 should be retained (no $ anchor in keep condition)")
	}
}
