package bus

import (
	"testing"
	"time"
)

// TestLastChangeAt_FirstSeenNotChange D-155：首见不算变化（重启场景锚）。
// 重启后首条报价落地 → LastChangeAt 返回 ok=false（从未变化），
// 防心跳报价落地即「新鲜」骗过 freeze guard（#464 正是重启冷启动即开仓）。
func TestLastChangeAt_FirstSeenNotChange(t *testing.T) {
	b := New()
	b.Publish(Quote{Symbol: "XAUUSD", Venue: "g", VenueType: "mt5", Bid: 2000, Ask: 2000.2, Time: time.Now()})

	if _, ok := b.LastChangeAt("XAUUSD", "g:mt5"); ok {
		t.Fatal("首见报价 lastChange 不应记录（首见不算变化），got ok=true")
	}
}

// TestLastChangeAt_HeartbeatNoUpdate D-155：同值心跳不更新 lastChangeAt。
// broker 推心跳报价（时间戳新、价格冻结）→ lastChange 不更新 → 执行侧识破骗线。
func TestLastChangeAt_HeartbeatNoUpdate(t *testing.T) {
	b := New()
	now := time.Now()
	b.Publish(Quote{Symbol: "XAUUSD", Venue: "g", VenueType: "mt5", Bid: 2000, Ask: 2000.2, Time: now})
	// 价格变化一次 → 记录
	b.Publish(Quote{Symbol: "XAUUSD", Venue: "g", VenueType: "mt5", Bid: 2001, Ask: 2001.2, Time: now.Add(time.Second)})
	t1, ok := b.LastChangeAt("XAUUSD", "g:mt5")
	if !ok {
		t.Fatal("价格变化后应记录 lastChange")
	}
	// 同值心跳（仅时间戳新）→ 不更新
	b.Publish(Quote{Symbol: "XAUUSD", Venue: "g", VenueType: "mt5", Bid: 2001, Ask: 2001.2, Time: now.Add(time.Minute)})
	t2, ok := b.LastChangeAt("XAUUSD", "g:mt5")
	if !ok {
		t.Fatal("心跳后 lastChange 仍应存在")
	}
	if !t2.Equal(t1) {
		t.Fatalf("同值心跳不应更新 lastChange: want %v, got %v", t1, t2)
	}
}

// TestLastChangeAt_PriceChangeUpdates D-155：价格值变化才更新 lastChangeAt。
// bid 或 ask 任一变化即视为价格变化（心跳报价两者均不变）。
func TestLastChangeAt_PriceChangeUpdates(t *testing.T) {
	b := New()
	now := time.Now()
	b.Publish(Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 50000, Ask: 50001, Time: now})
	if _, ok := b.LastChangeAt("BTCUSDT", "binance:spot"); ok {
		t.Fatal("首见不应记录 lastChange")
	}
	// bid 变化 → 记录
	b.Publish(Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 50010, Ask: 50001, Time: now.Add(time.Second)})
	if _, ok := b.LastChangeAt("BTCUSDT", "binance:spot"); !ok {
		t.Fatal("bid 变化应记录 lastChange")
	}
	// ask 变化 → 更新
	b.Publish(Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot", Bid: 50010, Ask: 50020, Time: now.Add(2 * time.Second)})
	t2, ok := b.LastChangeAt("BTCUSDT", "binance:spot")
	if !ok {
		t.Fatal("ask 变化应记录 lastChange")
	}
	if !t2.After(now) {
		t.Fatalf("价格变化后 lastChange 应推进: got %v (now=%v)", t2, now)
	}
}

// TestLastChangeAt_IsolatedPerVenueKey D-155：lastChange 按 (canonical, venueKey) 隔离，
// 不同 venue 的价格变化互不干扰（与 latest 同构）。
func TestLastChangeAt_IsolatedPerVenueKey(t *testing.T) {
	b := New()
	now := time.Now()
	// 两条腿不同 venue
	b.Publish(Quote{Symbol: "XAUUSD", Venue: "g", VenueType: "mt5", Bid: 2000, Ask: 2000.2, Time: now})
	b.Publish(Quote{Symbol: "XAUUSD", Venue: "p", VenueType: "mt5", Bid: 2000.1, Ask: 2000.3, Time: now})
	// 仅 g 腿价格变化
	b.Publish(Quote{Symbol: "XAUUSD", Venue: "g", VenueType: "mt5", Bid: 2001, Ask: 2001.2, Time: now.Add(time.Second)})

	if _, ok := b.LastChangeAt("XAUUSD", "g:mt5"); !ok {
		t.Fatal("g 腿价格变化应记录")
	}
	if _, ok := b.LastChangeAt("XAUUSD", "p:mt5"); ok {
		t.Fatal("p 腿未变化不应记录 lastChange")
	}
}
