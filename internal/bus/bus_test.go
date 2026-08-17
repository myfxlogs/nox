package bus

import (
	"testing"
	"time"
)

func TestQuoteVenueKey(t *testing.T) {
	q := Quote{Venue: "binance", VenueType: "spot"}
	if got := q.VenueKey(); got != "binance:spot" {
		t.Fatalf("VenueKey = %q, want binance:spot", got)
	}
}

func TestQuoteMid(t *testing.T) {
	q := Quote{Bid: 1.0, Ask: 1.2}
	if got := q.Mid(); got != 1.1 {
		t.Fatalf("Mid = %v, want 1.1", got)
	}
}

func TestQuoteBusPublishSubscribe(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe("BTCUSDT")
	defer cancel()

	q := Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 50000, Ask: 50001, Time: time.Now()}
	b.Publish(q)

	select {
	case got := <-ch:
		if got.Bid != 50000 {
			t.Fatalf("Bid = %v, want 50000", got.Bid)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for quote")
	}
}

func TestQuoteBusSnapshot(t *testing.T) {
	b := New()
	b.Publish(Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 50000, Ask: 50001, Time: time.Now()})
	b.Publish(Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "perp", Bid: 50010, Ask: 50012, Time: time.Now()})

	snap := b.Snapshot(nil, []string{"BTCUSDT"})
	if len(snap) != 1 {
		t.Fatalf("snapshot len = %d, want 1", len(snap))
	}
	venues := snap["BTCUSDT"]
	if len(venues) != 2 {
		t.Fatalf("venues len = %d, want 2", len(venues))
	}
	if _, ok := venues["binance:spot"]; !ok {
		t.Fatal("missing binance:spot")
	}
	if _, ok := venues["binance:perp"]; !ok {
		t.Fatal("missing binance:perp")
	}
}

func TestQuoteBusLatest(t *testing.T) {
	b := New()
	b.Publish(Quote{Symbol: "ETHUSDT", Venue: "okx", VenueType: "spot", Bid: 3000, Ask: 3001, Time: time.Now()})

	q, ok := b.Latest("ETHUSDT", "okx:spot")
	if !ok {
		t.Fatal("not found")
	}
	if q.Bid != 3000 {
		t.Fatalf("Bid = %v, want 3000", q.Bid)
	}

	_, ok = b.Latest("ETHUSDT", "okx:perp")
	if ok {
		t.Fatal("should not find okx:perp")
	}
}

func TestQuoteBusDrainThenReplace(t *testing.T) {
	b := New()
	ch, cancel := b.Subscribe("X")
	defer cancel()

	// 填满 channel
	b.Publish(Quote{Symbol: "X", Bid: 1, Time: time.Now()})
	b.Publish(Quote{Symbol: "X", Bid: 2, Time: time.Now()})

	q := <-ch
	if q.Bid != 2 {
		t.Fatalf("recv Bid = %v, want 2", q.Bid)
	}
}
