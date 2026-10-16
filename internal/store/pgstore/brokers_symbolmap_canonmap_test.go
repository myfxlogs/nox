package pgstore

import (
	"context"
	"testing"
)

// DB 集成测试：LoadSymbolCanonMap/Resolve（D-558 续段符号归一化）。
// 需 NOX_TEST_PG_DSN，未设置时 skip（testStore 门禁）。

func TestSymbolCanonMapResolve(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedBrokerVenue(t, s, "vA")
	// native 变体行（XAUUSDm→XAUUSD）+ native==canonical 恒同行（BRENT→BRENT）
	// + '#' 变体行（ADAUSD#→ADAUSDT）。
	for _, m := range []struct{ bs, canon string }{
		{"XAUUSDm", "XAUUSD"},
		{"BRENT", "BRENT"},
		{"ADAUSD#", "ADAUSDT"},
	} {
		if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "vA", BrokerSymbol: m.bs, Canonical: m.canon, Source: "manual"}); err != nil {
			t.Fatalf("upsert %s: %v", m.bs, err)
		}
	}
	cm, err := s.LoadSymbolCanonMap(ctx)
	if err != nil {
		t.Fatalf("LoadSymbolCanonMap: %v", err)
	}
	cases := []struct {
		name       string
		venue, sym string
		wantCanon  string
		wantOK     bool
	}{
		{"native 变体解析", "vA", "XAUUSDm", "XAUUSD", true},
		{"native==canonical 同行", "vA", "BRENT", "BRENT", true},
		{"'#' 原生行精确解析", "vA", "ADAUSD#", "ADAUSDT", true},
		{"canonical 标签行解析（venue 参与）", "vA", "XAUUSD", "XAUUSD", true},
		{"venue 缺行=未映射", "vB", "XAUUSD", "", false},
		{"未知符号=未映射", "vA", "NGUSD.a", "", false},
	}
	for _, c := range cases {
		got, ok := cm.Resolve(c.venue, c.sym)
		if ok != c.wantOK || got != c.wantCanon {
			t.Errorf("%s: Resolve(%s,%s)=(%q,%v), want (%q,%v)", c.name, c.venue, c.sym, got, ok, c.wantCanon, c.wantOK)
		}
	}
	// nil 表=全未映射（fail-closed：discover 查询失败路径）。
	var nilMap *SymbolCanonMap
	if _, ok := nilMap.Resolve("vA", "XAUUSD"); ok {
		t.Error("nil map must resolve nothing (fail-closed)")
	}
}
