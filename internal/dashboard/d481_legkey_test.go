// d481_legkey_test.go —— D-481 S3：qbus 键错位普查锚。
// T1 marginLegNotionalUSD：oppID canonical=XAUUSD（mt×cex 型）、腿原生 XAU-USDT-SWAP、
// 报价挂采集键 XAUUSDT——命中现价非成交价锚回退。
// T2 LatestLeg：CEX 原生键优先/canonical 兜底/MT 不查 native。
package dashboard

import (
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// TestD481_MarginLegNotionalUSD_NativeKeyHit 错位场景（pos502 同构）：
// canonical=XAUUSD，腿原生 symbol=XAU-USDT-SWAP，报价只挂采集键 XAUUSDT@okx:perp
// → 名义须用现价 4300 非成交价锚 4000（修前 miss 静默回退成交价）。
func TestD481_MarginLegNotionalUSD_NativeKeyHit(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSDT", VenueSymbol: "XAU-USDT-SWAP", Venue: "okx", VenueType: "perp",
		Bid: 4299, Ask: 4301, Time: time.Now()})
	leg := pgstore.MarginOpenLeg{
		OppID: "XASSET_CARRY:XAUUSD:a:b", Venue: "okx:perp", Symbol: "XAU-USDT-SWAP",
		Qty: 1016, FilledQty: f(1.016), Price: 4000, // 成交价锚与现价刻意拉开
	}
	got, ok := marginLegNotionalUSD(qb, d434CS, leg)
	if !ok {
		t.Fatal("ok=false")
	}
	if want := 1.016 * 4300.0; got < want*0.99 || got > want*1.01 {
		t.Fatalf("notional=%.2f want≈%.2f（现价口径，非成交价锚）", got, want)
	}
}

// TestD481_LatestLeg MT 腿 native 兜底防反噬：MT venue 报价挂 canonical=XAUUSD，
// 腿原生 symbol=XAUUSDm——native 归一键不命中的情况下 canonical 仍须命中。
func TestD481_LatestLeg_MtCanonicalDirect(t *testing.T) {
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", VenueSymbol: "XAUUSDm", Venue: "3001157", VenueType: "mt5",
		Bid: 4299, Ask: 4301, Time: time.Now()})
	q, ok := qb.LatestLeg("XAUUSD", "XAUUSDm", "3001157:mt5")
	if !ok || q.Mid() != 4300 {
		t.Fatalf("MT canonical 直查应命中 mid=4300, got ok=%v q=%+v", ok, q)
	}
	// CEX 腿 native 空→canonical 兜底；双键均 miss→false
	if _, ok := qb.LatestLeg("BTCUSDT", "", "binance:perp"); ok {
		t.Fatal("空 native + canonical miss 应 false")
	}
	qb.Publish(bus.Quote{Symbol: "BTCUSDT", VenueSymbol: "BTCUSDT", Venue: "binance", VenueType: "perp",
		Bid: 99, Ask: 101, Time: time.Now()})
	if _, ok := qb.LatestLeg("BTCUSDT", "", "binance:perp"); !ok {
		t.Fatal("native 空 canonical 兜底应命中")
	}
}
