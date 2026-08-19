package dashboard

import (
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/reconcile"
)

// TestBrokerPositionToProto_FreshFrame：新鲜帧 → current_price=mid、fresh=true、pnl 透传、is_arb 判别（D-051 WP3）。
func TestBrokerPositionToProto_FreshFrame(t *testing.T) {
	o := reconcile.VenueOrder{
		Venue: "277842155", VenueKey: "277842155:mt5", Ticket: "1001",
		Symbol: "EURUSDm", Side: "BUY", Volume: 0.1, OpenPrice: 1.1000,
		Profit: 12.34, Swap: -0.5, Commission: 0.6, Comment: "arb",
		OpenedAt: time.Unix(1700000000, 0),
	}
	ttl := 60 * time.Second
	now := time.Unix(1700000030, 0)
	price := bus.Quote{Symbol: "EURUSD", Venue: "277842155", VenueType: "mt5", Bid: 1.1010, Ask: 1.1012, Time: now.Add(-time.Second)}

	pb := brokerPositionToProto(o, "EURUSD", price, true, ttl, now)
	if pb.CurrentPrice != 1.1011 {
		t.Errorf("fresh frame current_price want mid 1.1011, got %v", pb.CurrentPrice)
	}
	if !pb.Fresh {
		t.Errorf("fresh frame fresh want true, got false")
	}
	if pb.Pnl != 12.34 {
		t.Errorf("pnl passthrough want 12.34, got %v", pb.Pnl)
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
}

// TestBrokerPositionToProto_NotArb：非 ARB 源 comment（manual）→ is_arb=false，其余字段照常。
func TestBrokerPositionToProto_NotArb(t *testing.T) {
	o := reconcile.VenueOrder{
		Venue: "v", VenueKey: "v:mt4", Ticket: "42",
		Symbol: "EURUSD", Side: "SELL", Volume: 1.0, OpenPrice: 1.2000,
		Profit: -3.0, Comment: "manual",
	}
	pb := brokerPositionToProto(o, "", bus.Quote{}, false, time.Minute, time.Now())
	if pb.IsArb {
		t.Errorf("comment 'manual' is_arb want false, got true")
	}
	if pb.Canonical != "" {
		t.Errorf("empty canonical want '', got %q", pb.Canonical)
	}
}

// TestBrokerPositionToProto_StaleFrame（G3）：帧超陈旧 TTL → current_price=0、fresh=false（陈旧帧不冒充现价）。
func TestBrokerPositionToProto_StaleFrame(t *testing.T) {
	o := reconcile.VenueOrder{Venue: "v", VenueKey: "v:mt5", Ticket: "7", Symbol: "XAUUSD", Comment: "arb"}
	ttl := 60 * time.Second
	now := time.Unix(2000, 0)
	stale := bus.Quote{Symbol: "XAUUSD", Bid: 2000.0, Ask: 2000.2, Time: now.Add(-2 * ttl)}

	pb := brokerPositionToProto(o, "XAUUSD", stale, true, ttl, now)
	if pb.CurrentPrice != 0 {
		t.Errorf("stale frame current_price want 0, got %v", pb.CurrentPrice)
	}
	if pb.Fresh {
		t.Errorf("stale frame fresh want false, got true")
	}
}

// TestBrokerPositionToProto_NoCanonical：canonical 空 → current_price=0、fresh=false（无价诚实空态，不冒充）。
func TestBrokerPositionToProto_NoCanonical(t *testing.T) {
	o := reconcile.VenueOrder{Venue: "v", VenueKey: "v:mt5", Ticket: "8", Symbol: "SYMBOL_NOT_MAPPED", Comment: "arb"}
	// 即使有报价帧，canonical 空 = 无价源 → 诚实空态。
	pb := brokerPositionToProto(o, "", bus.Quote{}, true, time.Minute, time.Now())
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
	o := reconcile.VenueOrder{Venue: "v", VenueKey: "v:mt4", Ticket: "9", Symbol: "GBPUSD", Comment: ""}
	pb := brokerPositionToProto(o, "", bus.Quote{}, false, time.Minute, time.Now())
	if pb.OpenedAt != nil {
		t.Errorf("zero OpenedAt want nil proto opened_at, got %v", pb.OpenedAt)
	}
}
