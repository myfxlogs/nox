package pgstore

import (
	"context"
	"testing"
)

// DB 集成测试：BrokerSymbolCanonical 后缀变体覆盖（D-558 续段③）。
// 'm' 已有语义（裸名 facts × 'm' 尾缀映射行）；'#' 为本票补覆盖
// （SG 110660304 实拍 AUDCAD#→AUDCAD 型目录行）。精确名恒优先。

func TestBrokerSymbolCanonicalSuffixVariants(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE broker_symbol_map`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	seedBrokerVenue(t, s, "vA")
	for _, m := range []struct{ bs, canon string }{
		{"XAUUSDm", "XAUUSD"}, // 'm' 变体行
		{"AUDCAD#", "AUDCAD"}, // '#' 变体行（裸名无独立行形态）
		{"BRENT", "BRENT"},    // 精确同行
	} {
		if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{Venue: "vA", BrokerSymbol: m.bs, Canonical: m.canon, Source: "manual"}); err != nil {
			t.Fatalf("upsert %s: %v", m.bs, err)
		}
	}
	cases := []struct {
		name       string
		venue, sym string
		want       string
	}{
		{"'m' 裸名解析", "vA", "XAUUSD", "XAUUSD"},
		{"'#' 裸名解析", "vA", "AUDCAD", "AUDCAD"},
		{"精确名优先", "vA", "BRENT", "BRENT"},
		{"未命中零值", "vA", "NGUSD", ""},
		{"venue 缺行零值", "vB", "XAUUSD", ""},
	}
	for _, c := range cases {
		got, err := s.BrokerSymbolCanonical(ctx, c.venue, c.sym)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
