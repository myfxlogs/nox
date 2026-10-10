package dashboard

import (
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/venue"
)

// TestBrokerPositionToProto_FreshFrame：新鲜帧 → current_price=mid、fresh=true、
// pnl=venue 透传权威值（D-116：移除 F5 重算，MT5 Profit 已换算 USD）、is_arb 判别（D-051 WP3）。
func TestBrokerPositionToProto_FreshFrame(t *testing.T) {
	o := venue.Order{
		Venue: "277842155", VenueKey: "277842155:mt5", Ticket: "1001",
		Symbol: "EURUSDm", Side: "BUY", Volume: 0.1, OpenPrice: 1.1000,
		Profit: 12.34, Swap: -0.5, Commission: 0.6, Comment: "arb",
		OpenedAt: time.Unix(1700000000, 0),
		Magic:    777, // D-052：MT5 ExpertId 透传
	}
	ttl := 60 * time.Second
	now := time.Unix(1700000030, 0)
	price := bus.Quote{Symbol: "EURUSD", Venue: "277842155", VenueType: "mt5", Bid: 1.1010, Ask: 1.1012, Time: now.Add(-time.Second)}

	pb := brokerPositionToProto(o, "EURUSD", price, true, true, nil, ttl, now, 0, nil, 0)
	if pb.CurrentPrice != 1.1010 { // D-009 S2：方向价——BUY 仓平仓吃 bid
		t.Errorf("fresh frame current_price want bid 1.1010, got %v", pb.CurrentPrice)
	}
	if pb.PriceBid != 1.1010 || pb.PriceAsk != 1.1012 {
		t.Errorf("price_bid/ask want 1.1010/1.1012, got %v/%v", pb.PriceBid, pb.PriceAsk)
	}
	if !pb.Fresh {
		t.Errorf("fresh frame fresh want true, got false")
	}
	// D-116：pnl 保持 venue 透传值（Order.Profit），不重算
	if pb.Pnl != 12.34 {
		t.Errorf("pnl passthrough want 12.34 (venue Profit), got %v", pb.Pnl)
	}
	if pb.Swap != -0.5 || pb.Commission != 0.6 {
		t.Errorf("swap/commission passthrough want -0.5/0.6, got %v/%v", pb.Swap, pb.Commission)
	}
	if !pb.IsArb {
		t.Errorf("comment 'arb' is_arb want true, got false")
	}
	if pb.OpenedAt == nil || !pb.OpenedAt.AsTime().Equal(o.OpenedAt) {
		t.Errorf("opened_at want %v, got %v", o.OpenedAt, pb.OpenedAt)
	}
	if pb.Magic != 777 {
		t.Errorf("magic passthrough want 777, got %v", pb.Magic)
	}
	if !pb.VenueOnline {
		t.Errorf("online venue_online want true, got false")
	}
}

// TestBrokerPositionToProto_OfflineMarked：离线 venue 保留快照 → venue_online=false 诚实标注（D-052）。
func TestBrokerPositionToProto_OfflineMarked(t *testing.T) {
	o := venue.Order{Venue: "v", VenueKey: "v:mt5", Ticket: "6", Symbol: "EURUSD", Comment: "arb", Magic: 3}
	pb := brokerPositionToProto(o, "EURUSD", bus.Quote{}, false, false, nil, time.Minute, time.Now(), 0, nil, 0)
	if pb.VenueOnline {
		t.Errorf("offline venue_online want false, got true")
	}
	if pb.Magic != 3 {
		t.Errorf("magic passthrough want 3, got %v", pb.Magic)
	}
}

// TestBrokerPositionToProto_NotArb：非 ARB 源 comment（manual）→ is_arb=false，其余字段照常。
func TestBrokerPositionToProto_NotArb(t *testing.T) {
	o := venue.Order{
		Venue: "v", VenueKey: "v:mt4", Ticket: "42",
		Symbol: "EURUSD", Side: "SELL", Volume: 1.0, OpenPrice: 1.2000,
		Profit: -3.0, Comment: "manual",
	}
	pb := brokerPositionToProto(o, "", bus.Quote{}, false, true, nil, time.Minute, time.Now(), 0, nil, 0)
	if pb.IsArb {
		t.Errorf("comment 'manual' is_arb want false, got true")
	}
	if pb.Canonical != "" {
		t.Errorf("empty canonical want '', got %q", pb.Canonical)
	}
}

// TestBrokerPositionToProto_StaleFrame（D-162 修订）：帧超 TTL → 价字段照填+price_at
// 真钟（休市末价诚实展示），fresh=false（stale 不冒充现价；D-167 后 est 不吃 Fresh 门控——价在即算）。
func TestBrokerPositionToProto_StaleFrame(t *testing.T) {
	o := venue.Order{Venue: "v", VenueKey: "v:mt5", Ticket: "7", Symbol: "XAUUSD", Comment: "arb"}
	ttl := 60 * time.Second
	now := time.Unix(2000, 0)
	stale := bus.Quote{Symbol: "XAUUSD", Bid: 2000.0, Ask: 2000.2, Time: now.Add(-2 * ttl)}

	pb := brokerPositionToProto(o, "XAUUSD", stale, true, true, nil, ttl, now, 0, nil, 0)
	if pb.CurrentPrice != 2000.0 { // BUY 仓平仓方向价=bid
		t.Errorf("stale frame current_price want 2000（休市末价照显）, got %v", pb.CurrentPrice)
	}
	if pb.Fresh {
		t.Errorf("stale frame fresh want false, got true")
	}
	if pb.PriceAt == nil || !pb.PriceAt.AsTime().Equal(stale.Time) {
		t.Errorf("stale frame price_at want %v, got %v", stale.Time, pb.PriceAt)
	}
}

// TestD162_NoPrice_StillEmpty：全无报价 → 照旧空态（current_price=0、price_at=nil）。
func TestD162_NoPrice_StillEmpty(t *testing.T) {
	o := venue.Order{Venue: "v", VenueKey: "v:mt5", Ticket: "9", Symbol: "XAUUSD", Comment: "arb"}
	pb := brokerPositionToProto(o, "XAUUSD", bus.Quote{}, false, true, nil, time.Minute, time.Now(), 0, nil, 0)
	if pb.CurrentPrice != 0 || pb.Fresh || pb.PriceAt != nil {
		t.Errorf("no price want all-empty, got cp=%v fresh=%v at=%v", pb.CurrentPrice, pb.Fresh, pb.PriceAt)
	}
}

// TestBrokerPositionToProto_NoCanonical：canonical 空 → current_price=0、fresh=false（无价诚实空态，不冒充）。
func TestBrokerPositionToProto_NoCanonical(t *testing.T) {
	o := venue.Order{Venue: "v", VenueKey: "v:mt5", Ticket: "8", Symbol: "SYMBOL_NOT_MAPPED", Comment: "arb"}
	// 即使有报价帧，canonical 空 = 无价源 → 诚实空态。
	pb := brokerPositionToProto(o, "", bus.Quote{}, true, true, nil, time.Minute, time.Now(), 0, nil, 0)
	if pb.CurrentPrice != 0 {
		t.Errorf("no canonical current_price want 0, got %v", pb.CurrentPrice)
	}
	if pb.Fresh {
		t.Errorf("no canonical fresh want false, got true")
	}
	if pb.Canonical != "" {
		t.Errorf("no canonical want '', got %q", pb.Canonical)
	}
}

// TestBrokerPositionToProto_OpenedAtZero：开仓时间零值（venue 未返回）→ proto opened_at nil。
func TestBrokerPositionToProto_OpenedAtZero(t *testing.T) {
	o := venue.Order{Venue: "v", VenueKey: "v:mt4", Ticket: "9", Symbol: "GBPUSD", Comment: ""}
	pb := brokerPositionToProto(o, "", bus.Quote{}, false, true, nil, time.Minute, time.Now(), 0, nil, 0)
	if pb.OpenedAt != nil {
		t.Errorf("zero OpenedAt want nil proto opened_at, got %v", pb.OpenedAt)
	}
}
