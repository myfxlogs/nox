package dashboard

// D-311 S8 锚②③⑦：resolveLegs 跨 canonical（卖腿 canonical_sell 报价+CS）+
// CanonicalSell=nil 回退 + 同所跨 canonical 防静默错腿 + legContractSizeOf :perp 四档。

import (
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

func d311strp(s string) *string { return &s }

// d311XAssetService 测试桩：qbus + 双腿 CS/step 解析器（cexContractSize 按
// (bareVenue, venueSymbol) 查 ct_val；legContractSize 按 venueKey 查 MT CS）。
func d311XAssetService(b *bus.QuoteBus, cexCS, mtCS map[string]float64) *Service {
	return &Service{
		qbus: b,
		cexContractSize: func(venue, symbol string) (float64, bool) {
			v, ok := cexCS[venue+"|"+symbol]
			return v, ok
		},
		legContractSize: func(venueKey, symbol string) (float64, bool) {
			v, ok := mtCS[venueKey+"|"+symbol]
			return v, ok
		},
		// 运行时 step 表直通（绕开 fallback 白名单——CL_USDT/PAXGUSDT 等不在硬编码表）。
		stepMeta: &cexStepMeta{rt: map[string]float64{
			"CL_USDT": 0.01, "BTCUSDT": 0.001, "BTC_USDT": 0.001,
			"XAUTUSDT": 0.01, "PAXGUSDT": 0.01,
		}},
	}
}

// TestD311_ResolveLegs_CrossCanonical S8锚②：buy=WTIUSD@EXR3001157:mt5（canonical 列），
// sell=BTCUSDT@gate:perp（canonical_sell 列）——双腿各自 canonical 取报价/CS。
// 修前双腿同 canonical：卖腿在 BTCUSDT 命名空间下查不到（或查错）报价。
func TestD311_ResolveLegs_CrossCanonical(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "WTIUSD", Venue: "EXR3001157", VenueType: "mt5", Bid: 40.0, Ask: 40.05, Time: now, VenueSymbol: "WTIUSD"})
	b.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now, VenueSymbol: "BTC_USDT"})
	s := d311XAssetService(b,
		map[string]float64{"gate|BTC_USDT": 0.01},
		map[string]float64{"EXR3001157:mt5|WTIUSD": 100},
	)
	opp := &pgstore.Opportunity{
		Canonical: "WTIUSD", CanonicalSell: d311strp("BTCUSDT"),
		VenueBuy: "EXR3001157:mt5", VenueSell: "gate:perp",
	}
	legs, err := s.resolveLegs(opp, 0.1)
	if err != nil {
		t.Fatalf("resolveLegs: %v", err)
	}
	if len(legs) != 2 {
		t.Fatalf("want 2 legs, got %d", len(legs))
	}
	buy, sell := legs[0], legs[1]
	if buy.VenueSymbol != "WTIUSD" || buy.Side != "BUY" || buy.VenueKey != "EXR3001157:mt5" {
		t.Errorf("buy leg wrong: %+v", buy)
	}
	if buy.PriceExpected != 40.05 {
		t.Errorf("buy PriceExpected want 40.05 (ask), got %v", buy.PriceExpected)
	}
	// 卖腿必须取 canonical_sell=BTCUSDT 的报价与符号（等名义反算：0.1×100/0.01=1000）。
	if sell.VenueSymbol != "BTC_USDT" || sell.Side != "SELL" || sell.VenueKey != "gate:perp" {
		t.Errorf("sell leg wrong: %+v", sell)
	}
	if sell.Qty != 1000 {
		t.Errorf("sell qty want 1000 (0.1×CS100/CS0.01), got %v", sell.Qty)
	}
	if sell.PriceExpected != 100000 {
		t.Errorf("sell PriceExpected want 100000 (bid), got %v", sell.PriceExpected)
	}
}

// TestD311_ResolveLegs_CanonicalSellNilFallback S8锚②回退：CanonicalSell=nil →
// 双腿同 canonical（向后兼容，SWAPARB 形态不变）。
func TestD311_ResolveLegs_CanonicalSellNilFallback(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "perp", Bid: 100000, Ask: 100010, Time: now, VenueSymbol: "BTCUSDT"})
	b.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 99990, Ask: 100000, Time: now, VenueSymbol: "BTC_USDT"})
	s := d311XAssetService(b,
		map[string]float64{"binance|BTCUSDT": 1.0, "gate|BTC_USDT": 0.01},
		nil,
	)
	opp := &pgstore.Opportunity{
		Canonical: "BTCUSDT", CanonicalSell: nil,
		VenueBuy: "binance:perp", VenueSell: "gate:perp",
	}
	legs, err := s.resolveLegs(opp, 0.1)
	if err != nil {
		t.Fatalf("resolveLegs: %v", err)
	}
	if legs[1].VenueSymbol != "BTC_USDT" {
		t.Errorf("sell VenueSymbol want BTC_USDT, got %s", legs[1].VenueSymbol)
	}
	// sellQty = 0.1×1.0/0.01 = 10（名义均衡口径按买腿参考价）。
	if legs[1].Qty != 10 {
		t.Errorf("sell qty want 10, got %v", legs[1].Qty)
	}
}

// TestD311_ResolveLegs_SameVenueCrossCanonical S8锚⑦：同所跨 canonical——
// buy=XAUTUSDT sell=PAXGUSDT 同 @binance:perp。修前卖腿静默建成 XAUTUSDT 同码错腿
// （双腿同 symbol 自对冲+白烧手续费，且能过 validateLegQtys/freezeGuard）。
// 断言卖腿 VenueSymbol=PAXGUSDT。
func TestD311_ResolveLegs_SameVenueCrossCanonical(t *testing.T) {
	b := bus.New()
	now := time.Now()
	b.Publish(bus.Quote{Symbol: "XAUTUSDT", Venue: "binance", VenueType: "perp", Bid: 4000, Ask: 4000.2, Time: now, VenueSymbol: "XAUTUSDT"})
	b.Publish(bus.Quote{Symbol: "PAXGUSDT", Venue: "binance", VenueType: "perp", Bid: 3999, Ask: 3999.2, Time: now, VenueSymbol: "PAXGUSDT"})
	s := d311XAssetService(b,
		map[string]float64{"binance|XAUTUSDT": 1.0, "binance|PAXGUSDT": 1.0},
		nil,
	)
	opp := &pgstore.Opportunity{
		Canonical: "XAUTUSDT", CanonicalSell: d311strp("PAXGUSDT"),
		VenueBuy: "binance:perp", VenueSell: "binance:perp",
	}
	legs, err := s.resolveLegs(opp, 0.1)
	if err != nil {
		t.Fatalf("resolveLegs: %v", err)
	}
	if legs[1].VenueSymbol != "PAXGUSDT" {
		t.Errorf("卖腿建成错腿：want PAXGUSDT, got %s（同码自对冲缺陷回归）", legs[1].VenueSymbol)
	}
	if legs[1].PriceExpected != 3999 {
		t.Errorf("sell PriceExpected want 3999 (PAXGUSDT bid), got %v", legs[1].PriceExpected)
	}
	if legs[0].VenueSymbol != "XAUTUSDT" {
		t.Errorf("buy VenueSymbol want XAUTUSDT, got %s", legs[0].VenueSymbol)
	}
}

// TestD311_LegContractSizeOf_Perp S8锚③：:perp 腿 ct_val 四档——
// gate 0.01（quanto_multiplier）/ okx 0.01（ctVal）/ binance 1.0 / 缺行→error。
// 另：:spot=1.0 回归、解析器未接线→error、MT 缺失→error（均 fail-closed 不猜）。
func TestD311_LegContractSizeOf_Perp(t *testing.T) {
	cexCS := map[string]float64{
		"gate|CL_USDT":       0.01,
		"okx|BTC-USDT-SWAP":  0.01,
		"binance|BTCUSDT":    1.0,
		"gate|NEGATIVE_USDT": -1.0, // 非正值 → error
	}
	s := &Service{cexContractSize: func(venue, symbol string) (float64, bool) {
		v, ok := cexCS[venue+"|"+symbol]
		return v, ok
	}}
	for _, tc := range []struct {
		venueKey, symbol string
		want             float64
	}{
		{"gate:perp", "CL_USDT", 0.01},
		{"okx:perp", "BTC-USDT-SWAP", 0.01},
		{"binance:perp", "BTCUSDT", 1.0},
	} {
		got, err := s.legContractSizeOf(tc.venueKey, tc.symbol)
		if err != nil {
			t.Errorf("%s %s: unexpected err %v", tc.venueKey, tc.symbol, err)
		}
		if got != tc.want {
			t.Errorf("%s %s: want %.4f, got %.4f", tc.venueKey, tc.symbol, tc.want, got)
		}
	}
	// 缺行/非正值 → error（fail-closed）
	for _, tc := range []struct{ venueKey, symbol string }{
		{"gate:perp", "NOPE_USDT"},
		{"gate:perp", "NEGATIVE_USDT"},
	} {
		if _, err := s.legContractSizeOf(tc.venueKey, tc.symbol); err == nil {
			t.Errorf("%s %s: want error (fail-closed), got nil", tc.venueKey, tc.symbol)
		}
	}
	// :spot 维持 1.0（CEX 现货 qty=base）
	if cs, err := s.legContractSizeOf("binance:spot", "BTCUSDT"); err != nil || cs != 1.0 {
		t.Errorf("spot leg: want (1.0,nil), got (%.4f,%v)", cs, err)
	}
	// 解析器未接线 → :perp error（不猜 1.0——旧版缺陷根因）
	s2 := &Service{}
	if _, err := s2.legContractSizeOf("gate:perp", "CL_USDT"); err == nil ||
		!strings.Contains(err.Error(), "解析器未接线") {
		t.Errorf("unwired resolver: want '解析器未接线', got %v", err)
	}
}
