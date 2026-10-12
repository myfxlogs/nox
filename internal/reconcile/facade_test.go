package reconcile

import (
	"arb/internal/venue"
	"context"
	"testing"
)

// fakeVenueQuerier 用于门面单测（修复⑫ S1.3）。
type fakeVenueQuerier struct {
	venueKey string
	orders   []venue.Order
	err      error
}

func (q *fakeVenueQuerier) VenueKey() string { return q.venueKey }
func (q *fakeVenueQuerier) QueryOpenedOrders(ctx context.Context) ([]venue.Order, error) {
	return q.orders, q.err
}

// TestOpenedByVenueKey_Hit 注册两 venue → 按 venueKey 命中各自（修复⑫ S1.3）。
func TestOpenedByVenueKey_Hit(t *testing.T) {
	r := New(nil,
		&fakeVenueQuerier{venueKey: "ic:mt5", orders: []venue.Order{{Ticket: "T1", Symbol: "EURUSD", Side: "BUY", Volume: 0.1}}},
		&fakeVenueQuerier{venueKey: "ex:mt5", orders: []venue.Order{{Ticket: "T2", Symbol: "GBPUSD", Side: "SELL", Volume: 0.2}}},
	)
	ctx := context.Background()

	got, err := r.OpenedByVenueKey(ctx, "ic:mt5")
	if err != nil {
		t.Fatalf("ic:mt5: %v", err)
	}
	if len(got) != 1 || got[0].Ticket != "T1" {
		t.Fatalf("ic:mt5 want T1, got %v", got)
	}

	got2, err := r.OpenedByVenueKey(ctx, "ex:mt5")
	if err != nil {
		t.Fatalf("ex:mt5: %v", err)
	}
	if len(got2) != 1 || got2[0].Ticket != "T2" {
		t.Fatalf("ex:mt5 want T2, got %v", got2)
	}
}

// TestOpenedByVenueKey_Unregistered 未注册 venue（含 CEX）→ error（调用方 fail-open）。
func TestOpenedByVenueKey_Unregistered(t *testing.T) {
	r := New(nil, &fakeVenueQuerier{venueKey: "ic:mt5"})
	_, err := r.OpenedByVenueKey(context.Background(), "binance:spot")
	if err == nil {
		t.Fatal("unregistered venueKey want error, got nil")
	}
}

// TestHistoryByVenueKey_Hit 注册历史查证器 → 按 venueKey+ticket 命中。
func TestHistoryByVenueKey_Hit(t *testing.T) {
	r := New(nil)
	r.SetOrderHistoryQueriers(&fakeHistoryQuerier{
		venueKey: "ic:mt5",
		results: map[string]*HistoryOrder{
			"T1": {Ticket: "T1", ClosePrice: 1.1, Profit: 100, Swap: 5, Commission: 2},
		},
	})
	h, err := r.HistoryByVenueKey(context.Background(), "ic:mt5", "T1", 30)
	if err != nil {
		t.Fatalf("ic:mt5 T1: %v", err)
	}
	if h == nil || h.Ticket != "T1" || h.Profit != 100 {
		t.Fatalf("want T1 profit=100, got %v", h)
	}
}

// TestHistoryByVenueKey_Unregistered 未注册 venue → (nil, nil)（同步公式兜底）。
func TestHistoryByVenueKey_Unregistered(t *testing.T) {
	r := New(nil)
	h, err := r.HistoryByVenueKey(context.Background(), "binance:spot", "T1", 30)
	if err != nil {
		t.Fatalf("unregistered want nil err, got %v", err)
	}
	if h != nil {
		t.Fatalf("unregistered want nil HistoryOrder, got %v", h)
	}
}

// TestHistoryByVenueKey_Miss 注册但 ticket 未命中 → (nil, nil)。
func TestHistoryByVenueKey_Miss(t *testing.T) {
	r := New(nil)
	r.SetOrderHistoryQueriers(&fakeHistoryQuerier{venueKey: "ic:mt5", results: map[string]*HistoryOrder{}})
	h, err := r.HistoryByVenueKey(context.Background(), "ic:mt5", "NOPE", 30)
	if err != nil {
		t.Fatalf("miss want nil err, got %v", err)
	}
	if h != nil {
		t.Fatalf("miss want nil, got %v", h)
	}
}
