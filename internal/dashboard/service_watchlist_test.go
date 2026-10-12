package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// TestWatchlistHitQuote_未初始化返回false D-090 返修包① R1：缓存未初始化时 watchlistHitQuote 应返回 false。
func TestWatchlistHitQuote_未初始化返回false(t *testing.T) {
	s := &Service{}
	q := bus.Quote{Symbol: "AUDJPY", Venue: "icmarkets", VenueType: "mt5"}
	if s.watchlistHitQuote(q) {
		t.Error("未初始化缓存 watchlistHitQuote 应返回 false，got true")
	}
}

// TestWatchlistHitQuote_双维过滤 D-090 返修包① R1：venue 过滤命中/不命中/空=全部。
// 直接构造 watchlistSet（不经 DB），验证 watchlistHitQuote 三态。
func TestWatchlistHitQuote_双维过滤(t *testing.T) {
	s := &Service{
		watchlistSet: map[string]map[string]struct{}{
			"AUDJPY":  {"icmarkets:mt5": {}, "fxts:mt5": {}}, // 限定两源
			"BTCUSDT": {},                                    // 空 = 全部源
		},
		watchlistInitOnce: true,
	}
	tests := []struct {
		name string
		q    bus.Quote
		want bool
	}{
		{"symbol 命中 + venue 命中", bus.Quote{Symbol: "AUDJPY", Venue: "icmarkets", VenueType: "mt5"}, true},
		{"symbol 命中 + venue 不命中", bus.Quote{Symbol: "AUDJPY", Venue: "binance", VenueType: "spot"}, false},
		{"symbol 命中 + venues 空=全部", bus.Quote{Symbol: "BTCUSDT", Venue: "binance", VenueType: "spot"}, true},
		{"symbol 命中 + venues 空=全部（另一源）", bus.Quote{Symbol: "BTCUSDT", Venue: "okx", VenueType: "perp"}, true},
		{"symbol 不命中", bus.Quote{Symbol: "EURUSD", Venue: "icmarkets", VenueType: "mt5"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := s.watchlistHitQuote(tt.q); got != tt.want {
				t.Errorf("watchlistHitQuote(%+v) = %v, want %v", tt.q, got, tt.want)
			}
		})
	}
}

// TestPendingQuote_覆盖最新 D-090：case 1 蓄压 map 覆盖式语义——
// 同 (venueKey|symbol) 多帧只保留最新一帧（flush 时只出最新）。
func TestPendingQuote_覆盖最新(t *testing.T) {
	pending := make(map[string]bus.Quote)
	now := time.Now()
	q1 := bus.Quote{Symbol: "AUDJPY", Venue: "a", VenueType: "mt5", Bid: 90.0, Ask: 90.01, Time: now}
	q2 := bus.Quote{Symbol: "AUDJPY", Venue: "a", VenueType: "mt5", Bid: 91.0, Ask: 91.01, Time: now.Add(time.Millisecond)}
	key := q1.VenueKey() + "|" + q1.Symbol
	pending[key] = q1
	pending[key] = q2 // 覆盖
	if len(pending) != 1 {
		t.Errorf("蓄压覆盖后应只 1 条, got %d", len(pending))
	}
	if pending[key].Bid != 91.0 {
		t.Errorf("蓄压应保留最新帧, got bid %v, want 91.0", pending[key].Bid)
	}
}

// TestSetWatchlist_缓存即时生效_未知拒绝_双维 D-090 返修包① R1（DSN-gated，ARB_TEST_PG_SERVICE_DSN）：
// SetWatchlist 全程持锁——校验未知 canonical 整单拒绝（FailedPrecondition）+ 写库 + 同锁内更新缓存。
// 缓存即时生效：SetWatchlist 后 watchlistHitQuote 立即反映新清单（含 venue 过滤）。
// venues 不强校验：存非法 venue 不报错（设计 §9 裁定）。
func TestSetWatchlist_缓存即时生效_未知拒绝_双维(t *testing.T) {
	st := testServiceStore(t)
	s := &Service{store: st}
	ctx := context.Background()

	// 清表 + 初始化缓存
	if _, err := st.Pool().Exec(ctx, `TRUNCATE watchlist_symbols`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if err := s.ensureWatchlistLoaded(ctx); err != nil {
		t.Fatalf("ensureWatchlistLoaded: %v", err)
	}

	// 1. 未知 canonical 整单拒绝（FOOBAR 不在 allCanonicals）
	_, err := s.SetWatchlist(ctx, connect.NewRequest(&v1.SetWatchlistRequest{
		Items: []*v1.WatchlistItem{{Canonical: "AUDJPY"}, {Canonical: "FOOBAR"}},
	}))
	if err == nil {
		t.Fatal("未知 canonical 应整单拒绝, got nil")
	}
	if ce, ok := err.(*connect.Error); !ok || ce.Code() != connect.CodeFailedPrecondition {
		t.Errorf("应 FailedPrecondition, got %v", err)
	}
	// 拒绝后缓存不变（仍空）
	q := bus.Quote{Symbol: "AUDJPY", Venue: "icmarkets", VenueType: "mt5"}
	if s.watchlistHitQuote(q) {
		t.Error("拒绝后缓存不应含 AUDJPY")
	}

	// 2. 合法清单全量写入 + 缓存即时生效（含 venue 限定）
	wantItems := []*v1.WatchlistItem{
		{Canonical: "AUDJPY", Venues: []string{"icmarkets:mt5"}}, // 限定一源
		{Canonical: "USDJPY"}, // 全部源
	}
	resp, err := s.SetWatchlist(ctx, connect.NewRequest(&v1.SetWatchlistRequest{Items: wantItems}))
	if err != nil {
		t.Fatalf("SetWatchlist 合法: %v", err)
	}
	got := resp.Msg.GetItems()
	if len(got) != len(wantItems) {
		t.Errorf("回显长度 %d, want %d", len(got), len(wantItems))
	}

	// AUDJPY 限定 icmarkets:mt5 → 命中；binance:spot → 不命中
	qHit := bus.Quote{Symbol: "AUDJPY", Venue: "icmarkets", VenueType: "mt5"}
	if !s.watchlistHitQuote(qHit) {
		t.Error("AUDJPY icmarkets:mt5 应命中（venue 限定内）")
	}
	qMiss := bus.Quote{Symbol: "AUDJPY", Venue: "binance", VenueType: "spot"}
	if s.watchlistHitQuote(qMiss) {
		t.Error("AUDJPY binance:spot 不应命中（venue 限定外）")
	}
	// USDJPY venues 空 = 全部源 → 任意 venue 命中
	qAny := bus.Quote{Symbol: "USDJPY", Venue: "any", VenueType: "mt5"}
	if !s.watchlistHitQuote(qAny) {
		t.Error("USDJPY venues 空=全部源，任意 venue 应命中")
	}
	// EURUSD 未选
	qOut := bus.Quote{Symbol: "EURUSD", Venue: "icmarkets", VenueType: "mt5"}
	if s.watchlistHitQuote(qOut) {
		t.Error("EURUSD 未选不应命中")
	}

	// 3. venues 不强校验：存非法 venue 不报错（设计 §9 裁定）
	_, err = s.SetWatchlist(ctx, connect.NewRequest(&v1.SetWatchlistRequest{
		Items: []*v1.WatchlistItem{{Canonical: "AUDJPY", Venues: []string{"nonexistent:fake"}}},
	}))
	if err != nil {
		t.Errorf("venues 不强校验，非法 venue 不应报错, got %v", err)
	}

	// 4. 空清单=清空（合法）+ 缓存即时清空
	if _, err := s.SetWatchlist(ctx, connect.NewRequest(&v1.SetWatchlistRequest{Items: nil})); err != nil {
		t.Fatalf("SetWatchlist 清空: %v", err)
	}
	if s.watchlistHitQuote(qHit) {
		t.Errorf("清空后缓存不应含 AUDJPY")
	}
}
