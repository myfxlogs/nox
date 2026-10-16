package autotrade

// D-558 双断言（票验收锚）：等价键锚 canonical 产品类。
// ① 同 canonical 异组放行——SG 病灶形态实拍复刻：XAUUSD 两 venue product_group
//    异（15032313=fx / 3001157=cfd_spot，本地目录归法差异）但产品类同=metal
//    → 配对产出（旧 sameProductGroup 语义下全灭）。
// ② 真异品/未归类拒——canonical 无裁定行（fin_canonical_product_class 无行）
//    → 零产出 fail-closed（类异拒由 TestSameProductClass 纯函数锚）。

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// d558Seed 复刻 SG 病灶数据形态：两 venue 同 canonical、product_group 异、
// 类表只裁定 XAUUSD（WTI 留空=未归类对照）。
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
	// broker_symbol_map：同 canonical 跨 venue 异 product_group（SG 实拍归法差异入位，
	// 证明新判定不再消费本地目录值）。
	for _, m := range []struct{ venue, brokerSym, canonical, pg string }{
		{"15032313", "XAUUSD.fx", "XAUUSD", "fx"},       // 15032313 目录归 fx 区（SG 实拍）
		{"3001157", "XAUUSD.cfd", "XAUUSD", "cfd_spot"}, // 3001157 目录归 cfd_spot 区（SG 实拍）
		{"15032313", "WTI.spot", "WTI", "cfd_spot"},
		{"3001157", "WTI.lev", "WTI", "cfd_leverage"},
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
	// XAUUSD：同品异组（fx×cfd_spot）——断言①放行面。
	// WTI：未归类（类表无行）——断言②拒面。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindSwapLong, Venue: "15032313", Symbol: "XAUUSD", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "3001157", Symbol: "XAUUSD", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapLong, Venue: "15032313", Symbol: "WTI", Value: 5.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
		{Kind: fact.KindSwapShort, Venue: "3001157", Symbol: "WTI", Value: -3.0, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	e := &Engine{store: s, cfg: gate.Config{MinNetSwapBps: 0, SwapFactMaxAge: 2 * time.Hour}}
	combos := e.discoverSwapCombos(ctx)
	var xauusd, wti int
	for _, c := range combos {
		switch c.canonical {
		case "XAUUSD":
			xauusd++
		case "WTI":
			wti++
		}
	}
	if xauusd != 1 {
		t.Errorf("① 同 canonical 异组放行: XAUUSD combos=%d, want 1（product_group fx×cfd_spot 不得再拦截）", xauusd)
	}
	if wti != 0 {
		t.Errorf("② 未归类 fail-closed: WTI combos=%d, want 0（类表无行禁配对）", wti)
	}
}
