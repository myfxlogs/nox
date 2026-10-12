package bus

import (
	"context"
	"sync"
	"testing"
	"time"
)

// mockMappingStore 记录 DeleteSymbolMapping 调用（D-177 S2 单测）。
type mockMappingStore struct {
	mu      sync.Mutex
	deletes []struct{ venue, brokerSymbol string }
	failErr error
}

func (m *mockMappingStore) DeleteSymbolMapping(_ context.Context, venue, brokerSymbol string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deletes = append(m.deletes, struct{ venue, brokerSymbol string }{venue, brokerSymbol})
	return m.failErr
}

// TestQuoteGuard_OnQuote D-177 S2：8 case 覆盖报价量级校验。
func TestQuoteGuard_OnQuote(t *testing.T) {
	cases := []struct {
		name     string
		newMid   float64 // 新报价 mid
		refMid   float64 // 参照 venue mid（0=无参照）
		wantIso  bool    // 是否隔离（删映射+reload）
		wantSeen bool    // 是否标记 seen
	}{
		{"量级正常 ratio=1.0", 66, 66, false, true},
		{"正常价差 ratio=1.008", 66.5, 66, false, true},
		{"跨币种 ratio=1.39 隔离", 92, 66, true, true},
		{"跨币种2 ratio=1.43 隔离", 3800, 2650, true, true},
		{"跨品种 ratio=1.15 放行", 98, 85, false, true},
		{"无参照 放行+seen", 66, 0, false, true},
		{"mid<=0 跳过", 0, 66, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			bus := New()
			store := &mockMappingStore{}
			reloadCalled := false
			guard := NewQuoteGuard(bus, store, func(_ context.Context, _ string) {
				reloadCalled = true
			})

			// 先 publish 参照报价（另一个 venue）
			if c.refMid > 0 {
				bus.Publish(Quote{
					Symbol: "TEST", Venue: "refVenue", VenueType: "mt5",
					Bid: c.refMid - 0.01, Ask: c.refMid + 0.01, Time: time.Now(),
				})
			}

			// 等参照报价入 Snapshot
			if c.refMid > 0 {
				waitFor(t, time.Second, func() bool {
					snap := bus.Snapshot(context.Background(), []string{"TEST"})
					return len(snap["TEST"]) >= 1
				})
			}

			// publish 新报价（被测 venue）
			newBid := c.newMid - 0.01
			newAsk := c.newMid + 0.01
			if c.newMid <= 0 {
				newBid, newAsk = 0, 0
			}
			bus.Publish(Quote{
				Symbol: "TEST", Venue: "newVenue", VenueType: "mt5",
				VenueSymbol: "TESTbroker", Bid: newBid, Ask: newAsk, Time: time.Now(),
			})

			// 等新报价入 Snapshot（onQuote 通过 Subscribe wildcard 异步收到）
			if c.newMid > 0 {
				waitFor(t, time.Second, func() bool {
					snap := bus.Snapshot(context.Background(), []string{"TEST"})
					return len(snap["TEST"]) >= 2 || (c.refMid == 0 && len(snap["TEST"]) >= 1)
				})
			}

			// 调 onQuote 直接验证逻辑（异步 Subscribe 可能延迟）
			guard.onQuote(context.Background(), Quote{
				Symbol: "TEST", Venue: "newVenue", VenueType: "mt5",
				VenueSymbol: "TESTbroker", Bid: newBid, Ask: newAsk, Time: time.Now(),
			})

			// 验证 seen
			guard.mu.Lock()
			gotSeen := guard.seen["TEST|newVenue:mt5"]
			guard.mu.Unlock()
			if gotSeen != c.wantSeen {
				t.Errorf("seen: got %v, want %v", gotSeen, c.wantSeen)
			}

			// 验证隔离
			store.mu.Lock()
			gotDeletes := len(store.deletes)
			store.mu.Unlock()
			if c.wantIso {
				if gotDeletes != 1 {
					t.Errorf("deletes: got %d, want 1 (isolated)", gotDeletes)
				}
				if !reloadCalled {
					t.Error("reload should be called on isolation")
				}
			} else {
				if gotDeletes != 0 {
					t.Errorf("deletes: got %d, want 0 (not isolated)", gotDeletes)
				}
			}
		})
	}
}

// TestQuoteGuard_SeenSkips D-177 S2 C-2：已校验的 (canonical,venueKey) 后续报价跳过（不重复触发 log/隔离）。
func TestQuoteGuard_SeenSkips(t *testing.T) {
	bus := New()
	store := &mockMappingStore{}
	guard := NewQuoteGuard(bus, store, func(_ context.Context, _ string) {})

	// 先标记 seen
	guard.mu.Lock()
	guard.seen["TEST|newVenue:mt5"] = true
	guard.mu.Unlock()

	// publish 参照 + 新报价
	bus.Publish(Quote{Symbol: "TEST", Venue: "ref", VenueType: "mt5", Bid: 65, Ask: 67, Time: time.Now()})
	bus.Publish(Quote{Symbol: "TEST", Venue: "newVenue", VenueType: "mt5", VenueSymbol: "TESTbroker", Bid: 91, Ask: 93, Time: time.Now()})

	// 直接调 onQuote（seen 命中应跳过）
	guard.onQuote(context.Background(), Quote{
		Symbol: "TEST", Venue: "newVenue", VenueType: "mt5",
		VenueSymbol: "TESTbroker", Bid: 91, Ask: 93, Time: time.Now(),
	})

	store.mu.Lock()
	gotDeletes := len(store.deletes)
	store.mu.Unlock()
	if gotDeletes != 0 {
		t.Errorf("seen skip: deletes got %d, want 0 (seen should skip)", gotDeletes)
	}
}

// waitFor 轮询条件直到超时（测试 helper）。
func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}
