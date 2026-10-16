package autotrade

// D-558 + 续段（符号归一化）discover 级验收锚：
// ① 异名同 canonical 配对——facts 存 broker 本地名（15032313=XAUUSDm/XAUUSD.pro、
//    3001157=XAUUSD 三形态），product_group 异（fx×cfd_spot 本地目录归法差异），
//    归一化后同=metal → 恰 1 个 combo（旧组名判定全灭 + 裸名配对配不上双病灶修复）；
// ② 同 (venue,canonical) 多变体去重（XAUUSDm×XAUUSD.pro 两长腿行→1 combo）；
// ③ 类表无行 fail-closed（WTI：bsm 有行、fin_canonical_product_class 无行→unclassified 拒）；
// ④ 真未映射 fail-closed（NGUSD.a：无 broker_symbol_map 行→归一化丢弃）。

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

func d558Seed(t *testing.T, s *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	for _, q := range []string{
		`TRUNCATE facts`,
		`TRUNCATE broker_accounts CASCADE`,
		`TRUNCATE broker_symbol_map`,
		`TRUNCATE fin_canonical_product_class`,
	} {
		if _, err := s.Pool().Exec(ctx, q); err != nil {
			t.Fatalf("truncate: %v", err)
		}
	}
	// 产品类裁定登记册：只裁 XAUUSD→metal；WTI 无行=未归类对照。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO fin_canonical_product_class (canonical, product_class) VALUES ('XAUUSD','metal')`); err != nil {
		t.Fatalf("seed class: %v", err)
	}
	// 两 venue connected + 同型（demo×demo）——broker_accounts 先落（bsm FK 父行，D-496）。
	for _, v := range []string{"15032313", "3001157"} {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
			 VALUES ($1, 'mt5', 'h', 443, 'p', 's', 'connected', false)`, v); err != nil {
			t.Fatalf("seed %s: %v", v, err)
		}
	}
	// broker_symbol_map：同 canonical 跨 venue 异 broker 本地名 + 异 product_group
	// （SG 实拍形态：本地目录归法差异入位存档，配对判定不再消费组名）。
	for _, m := range []struct{ venue, brokerSym, canonical, pg string }{
		{"15032313", "XAUUSDm", "XAUUSD", "fx"},       // 'm' 变体，目录归 fx 区（SG 实拍）
		{"15032313", "XAUUSD.pro", "XAUUSD", "fx"},    // 第二变体（去重对照）
		{"3001157", "XAUUSD", "XAUUSD", "cfd_spot"},   // native==canonical，目录归 cfd_spot 区（SG 实拍）
		{"15032313", "WTI.spot", "WTI", "cfd_spot"},   // WTI：bsm 有行
		{"3001157", "WTI.lev", "WTI", "cfd_leverage"}, // 类表无行→unclassified
	} {
		if err := s.UpsertSymbolMapping(ctx, &pgstore.SymbolMapping{Venue: m.venue, BrokerSymbol: m.brokerSym, Canonical: m.canonical, Source: "manual"}); err != nil {
			t.Fatalf("upsert map %s: %v", m.brokerSym, err)
		}
		if _, err := s.UpdateProductGroupByCanonical(ctx, m.venue, m.canonical, m.pg); err != nil {
			t.Fatalf("set pg %s/%s: %v", m.venue, m.canonical, err)
		}
	}
}

func TestD558_DiscoverSwapCombos_ProductClassGate(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	d558Seed(t, s)
	// NGUSD.a 无 bsm 行=真未映射对照（归一化丢弃，禁配对）。
	// XAUUSD@15032313 canonical 标签行=生产 D-325 双发形态（swapRateFor spec 兜底按
	// canonical 名查 facts——治理值行命中；dedup 优先级锚同函数级测试）。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "15032313", Symbol: "XAUUSD", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "15032313", Symbol: "XAUUSDm", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "15032313", Symbol: "XAUUSD.pro", Value: 9.0, Unit: fact.UnitBpsDaily, Ts: now.Add(time.Second), Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "3001157", Symbol: "XAUUSD", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "15032313", Symbol: "WTI.spot", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "3001157", Symbol: "WTI.lev", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "15032313", Symbol: "NGUSD.a", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "3001157", Symbol: "NGUSD.a", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(ctx)
	if len(combos) != 1 {
		t.Fatalf("want 恰 1 combo（XAUUSD 异名配对+多变体去重；WTI unclassified/NGUSD unmapped 拒）, got %d: %+v", len(combos), combos)
	}
	c := combos[0]
	if c.canonical != "XAUUSD" || c.vLong != "15032313:mt5" || c.vShort != "3001157:mt5" {
		t.Errorf("combo 形态: got %+v, want XAUUSD 15032313×3001157", c)
	}
}
