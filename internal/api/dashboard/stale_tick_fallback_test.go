package dashboard

// D-162 锚：MT5 休市现价诚实化——
// ① 帧携 stale 价照显+Fresh=false+price_at 真钟（brokerPositionToProto 语义改）；
// ② 帧携价全缺 → ticks 末帧兜底（SetStaleTickFallback）；
// ③ 兜底读口 nil/无行 → 照旧诚实空态。

import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/venue"
)

// TestD162_StaleTickFallback 休市+重启形态：o.PriceAt=0（ClosePrice=0/无帧价）→
// ticks 末帧兜底出价（stale 如实，price_at=tick 钟）。
func TestD162_StaleTickFallback(t *testing.T) {
	m := NewBrokerPositionMonitor(nil, nil, 0, nil, ServerTZCache{}, nil, nil)
	tickTime := time.Now().Add(-20 * time.Hour) // 周五收市价
	m.SetStaleTickFallback(func(_ context.Context, venueKey, symbol string) (bus.Quote, bool) {
		if venueKey != "3001157:mt5" || symbol != "XAUUSDm" {
			return bus.Quote{}, false
		}
		return bus.Quote{Symbol: "XAUUSDm", Bid: 4194.85, Ask: 4195.11, Time: tickTime}, true
	})
	ord := venue.Order{Venue: "3001157", VenueKey: "3001157:mt5", Symbol: "XAUUSDm",
		Side: "BUY", Volume: 0.01, OpenPrice: 4180.0} // PriceAt=0=帧携价缺（休市形态）
	vs := venueState{orders: map[string]venue.Order{"t1": ord}, online: true}
	pb := m.protoFor(context.Background(), "3001157:mt5", vs, ord, time.Now())
	if pb.CurrentPrice != 4194.85 || pb.Fresh {
		t.Fatalf("fallback want cp=4194.85 fresh=false, got %v/%v", pb.CurrentPrice, pb.Fresh)
	}
	if pb.PriceAt == nil || !pb.PriceAt.AsTime().Equal(tickTime) {
		t.Fatalf("price_at want %v, got %v", tickTime, pb.PriceAt)
	}
}

// TestD162_FramePriceKept 帧携价在场 → 不吃兜底（staleTickFor 被调即错——帧价优先）。
func TestD162_FramePriceKept(t *testing.T) {
	m := NewBrokerPositionMonitor(nil, nil, time.Minute, nil, ServerTZCache{}, nil, nil)
	m.SetStaleTickFallback(func(context.Context, string, string) (bus.Quote, bool) {
		t.Fatal("帧携价在场不应触兜底")
		return bus.Quote{}, false
	})
	ord := venue.Order{Venue: "3001157", VenueKey: "3001157:mt5", Symbol: "XAUUSDm",
		Side: "BUY", Volume: 0.01, CurrentPrice: 4200.5, PriceAt: time.Now()}
	vs := venueState{orders: map[string]venue.Order{"t1": ord}, online: true}
	pb := m.protoFor(context.Background(), "3001157:mt5", vs, ord, time.Now())
	if pb.CurrentPrice != 4200.5 || !pb.Fresh {
		t.Fatalf("frame price want 4200.5 fresh, got %v/%v", pb.CurrentPrice, pb.Fresh)
	}
}

// TestD162_FallbackMiss_NilSafe 兜底未注入/无行 → 诚实空态（不 panic、不猜价）。
func TestD162_FallbackMiss_NilSafe(t *testing.T) {
	m := NewBrokerPositionMonitor(nil, nil, 0, nil, ServerTZCache{}, nil, nil)
	ord := venue.Order{Venue: "3001157", VenueKey: "3001157:mt5", Symbol: "XAUUSDm",
		Side: "BUY", Volume: 0.01}
	vs := venueState{orders: map[string]venue.Order{"t1": ord}, online: true}
	pb := m.protoFor(context.Background(), "3001157:mt5", vs, ord, time.Now())
	if pb.CurrentPrice != 0 || pb.Fresh || pb.PriceAt != nil {
		t.Fatalf("nil fallback want empty, got %v/%v/%v", pb.CurrentPrice, pb.Fresh, pb.PriceAt)
	}
	m.SetStaleTickFallback(func(context.Context, string, string) (bus.Quote, bool) { return bus.Quote{}, false })
	pb = m.protoFor(context.Background(), "3001157:mt5", vs, ord, time.Now())
	if pb.CurrentPrice != 0 || pb.Fresh {
		t.Fatalf("miss fallback want empty, got %v/%v", pb.CurrentPrice, pb.Fresh)
	}
}
