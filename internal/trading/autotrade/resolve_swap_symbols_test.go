package autotrade

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// TestResolveSwapSymbols D-558 续段：符号归一化+去重锚。
// ① canonical 标签行恒优先 native variant（swapFact Ts 每行 time.Now()，native 后发恒新
// ——纯 latest-ts 会系统性选中 native raw 丢 D-300 S0 治理值）；② 同类内取最新 ts；
// ③ 未映射丢弃；④ DB 映射解析（native 变体/canonical 标签/恒同行三形态）。
func TestResolveSwapSymbols(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE broker_symbol_map`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
		 VALUES ('vA', 'mt5', 'h', 443, 'p', 's', 'connected', false)`); err != nil {
		t.Fatalf("seed venue: %v", err)
	}
	for _, mm := range []struct{ bs, canon string }{
		{"XAUUSDm", "XAUUSD"}, // native 变体行
		{"BRENT", "BRENT"},    // native==canonical 恒同行
	} {
		if err := s.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{Venue: "vA", BrokerSymbol: mm.bs, Canonical: mm.canon, Source: "manual"}); err != nil {
			t.Fatalf("upsert %s: %v", mm.bs, err)
		}
	}
	cm, err := s.LoadSymbolCanonMap(ctx)
	if err != nil {
		t.Fatalf("LoadSymbolCanonMap: %v", err)
	}
	now := time.Now()
	long := resolveSwapSymbols([]fact.Fact{
		// canonical 标签行（治理值，先发）+ native raw（后发 ts 更新）——同 (venue,canonical)。
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "XAUUSD", Value: 5, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "XAUUSDm", Value: -99, Ts: now.Add(time.Second), Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "BRENT", Value: 1, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "vA", Symbol: "BRENT.missing", Value: 9, Ts: now, Src: "mt5"}, // 未映射
	}, cm)
	if len(long) != 2 {
		t.Fatalf("归一化+去重: want 2 (XAUUSD 去重合并+BRENT, 未映射丢弃), got %d: %+v", len(long), long)
	}
	for _, f := range long {
		switch f.Symbol {
		case "XAUUSD":
			if f.Value != 5 {
				t.Errorf("canonical 标签行须恒优先 native raw（D-300 S0 治理值）: got %v, want 5", f.Value)
			}
		case "BRENT":
		default:
			t.Errorf("意外符号 %q（未映射未丢弃或归一化失败）", f.Symbol)
		}
	}
	// 同类（都 native 变体）取最新 ts。
	short := resolveSwapSymbols([]fact.Fact{
		{Venue: "vA", Symbol: "XAUUSDm", Value: -3, Ts: now, Src: "mt5"},
		{Venue: "vA", Symbol: "XAUUSDm", Value: -7, Ts: now.Add(2 * time.Second), Src: "mt5"},
	}, cm)
	if len(short) != 1 || short[0].Value != -7 {
		t.Errorf("同类 native 取最新 ts: got %+v, want 单行 value=-7", short)
	}
	// nil 表=全未映射（discover 查询失败 fail-closed 路径）。
	if got := resolveSwapSymbols([]fact.Fact{{Venue: "vA", Symbol: "XAUUSD", Ts: now}}, nil); len(got) != 0 {
		t.Errorf("nil canonMap must drop all facts (fail-closed), got %+v", got)
	}
}
