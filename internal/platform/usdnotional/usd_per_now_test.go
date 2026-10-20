package usdnotional

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
)

func pub(b *bus.QuoteBus, sym, venue, vt string, bid float64, age time.Duration) {
	b.Publish(bus.Quote{Symbol: sym, Venue: venue, VenueType: vt, Bid: bid, Ask: bid + 0.001, Time: time.Now().Add(-age)})
}

// D-633 三态锚：prefer 命中/prefer 陈→择鲜他家/全 miss fail-closed+MT 键域滤 CEX+invert 双向。
func TestUSDPerNow_ThreeStates(t *testing.T) {
	ctx := context.Background()

	// ① prefer 命中（本仓 venueKey 优先——EURUSD quote 直取）
	b := bus.New()
	pub(b, "EURUSD", "15032313", "mt5", 1.08, 0)
	pub(b, "EURUSD", "3001157", "mt5", 1.09, 0) // 他家价异——prefer 必须取 1.08
	if r, ok := USDPerNow(ctx, b, "EUR", "15032313:mt5"); !ok || r != 1.08 {
		t.Fatalf("prefer 命中错: %v %v", r, ok)
	}

	// ② prefer 陈帧→落择鲜径取他家（D1 鲜度检——陈 prefer 不得被真金径收）
	b2 := bus.New()
	pub(b2, "EURUSD", "15032313", "mt5", 1.08, 10*time.Minute) // prefer 陈
	pub(b2, "EURUSD", "3001157", "mt5", 1.09, 0)               // 他家鲜
	if r, ok := USDPerNow(ctx, b2, "EUR", "15032313:mt5"); !ok || r != 1.09 {
		t.Fatalf("prefer 陈应落择鲜他家 1.09: %v %v", r, ok)
	}

	// ③ 全 miss=fail-closed（空 qbus + 非七直盘 + CEX 键滤除）
	b3 := bus.New()
	if _, ok := USDPerNow(ctx, b3, "EUR", ""); ok {
		t.Fatal("空 qbus 应 fail-closed")
	}
	pub(b3, "EURUSD", "binance", "spot", 1.08, 0) // CEX 键——MT 键域滤除
	if _, ok := USDPerNow(ctx, b3, "EUR", ""); ok {
		t.Fatal("CEX 键应被 MT 键域滤除 fail-closed")
	}
	if _, ok := USDPerNow(ctx, b3, "XXX", ""); ok {
		t.Fatal("非七直盘应 fail-closed")
	}

	// ④ invert 双向：JPY=USD-base→1/px；GBP=USD-quote→直取
	b4 := bus.New()
	pub(b4, "USDJPY", "15032313", "mt5", 150.0, 0)
	pub(b4, "GBPUSD", "15032313", "mt5", 1.25, 0)
	if r, ok := USDPerNow(ctx, b4, "JPY", ""); !ok || r != 1.0/150.0 {
		t.Fatalf("JPY invert 错: %v", r)
	}
	if r, ok := USDPerNow(ctx, b4, "GBP", ""); !ok || r != 1.25 {
		t.Fatalf("GBP 直取错: %v", r)
	}
}
