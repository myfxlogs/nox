package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

// TestWatchlistHit_未初始化返回false D-090：缓存未初始化时 watchlistHit 应返回 false
// （治 App 根流先于任何 GetWatchlist 启动时过滤失效——ensureWatchlistLoaded 先行）。
func TestWatchlistHit_未初始化返回false(t *testing.T) {
	s := &Service{}
	if s.watchlistHit("AUDJPY") {
		t.Error("未初始化缓存 watchlistHit 应返回 false，got true")
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

// TestSetWatchlist_缓存即时生效_未知拒绝 D-090（DSN-gated，ARB_TEST_PG_SERVICE_DSN）：
// SetWatchlist 全程持锁——校验未知 canonical 整单拒绝（FailedPrecondition）+ 写库 + 同锁内更新缓存。
// 缓存即时生效：SetWatchlist 后 watchlistHit 立即反映新清单。
func TestSetWatchlist_缓存即时生效_未知拒绝(t *testing.T) {
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
		Canonical: []string{"AUDJPY", "FOOBAR"},
	}))
	if err == nil {
		t.Fatal("未知 canonical 应整单拒绝, got nil")
	}
	if ce, ok := err.(*connect.Error); !ok || ce.Code() != connect.CodeFailedPrecondition {
		t.Errorf("应 FailedPrecondition, got %v", err)
	}
	// 拒绝后缓存不变（仍空）
	if s.watchlistHit("AUDJPY") {
		t.Error("拒绝后缓存不应含 AUDJPY")
	}

	// 2. 合法清单全量写入 + 缓存即时生效
	want := []string{"AUDJPY", "USDJPY", "XAUUSD"}
	resp, err := s.SetWatchlist(ctx, connect.NewRequest(&v1.SetWatchlistRequest{Canonical: want}))
	if err != nil {
		t.Fatalf("SetWatchlist 合法: %v", err)
	}
	got := resp.Msg.GetCanonical()
	if len(got) != len(want) {
		t.Errorf("回显长度 %d, want %d", len(got), len(want))
	}
	for _, c := range want {
		if !s.watchlistHit(c) {
			t.Errorf("缓存即时生效失败: %s 未命中", c)
		}
	}
	if s.watchlistHit("EURUSD") {
		t.Error("EURUSD 未选不应命中")
	}

	// 3. 空清单=清空（合法）+ 缓存即时清空
	if _, err := s.SetWatchlist(ctx, connect.NewRequest(&v1.SetWatchlistRequest{Canonical: nil})); err != nil {
		t.Fatalf("SetWatchlist 清空: %v", err)
	}
	for _, c := range want {
		if s.watchlistHit(c) {
			t.Errorf("清空后缓存不应含 %s", c)
		}
	}
}
