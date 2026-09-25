package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/bus"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// SetQuoteFlushInterval 注入 StreamUpdates case 1 蓄压 flush 间隔（D-090）。
// main 装配后调；缺省 1s（看盘条足够，无信息损失——最新值语义）；测试注 10ms。
func (s *Service) SetQuoteFlushInterval(d time.Duration) { s.quoteFlushInterval = d }

// ensureWatchlistLoaded 惰性加载 watchlist 缓存（D-090 §4 + 返修包① R1 双维）。
// 首次调用查库填 watchlistSet（per-item venue 集合）+ 置 watchlistInitOnce=true。
// 全程持写锁（与 SetWatchlist 互斥）。
func (s *Service) ensureWatchlistLoaded(ctx context.Context) error {
	s.watchlistMu.Lock()
	defer s.watchlistMu.Unlock()
	if s.watchlistInitOnce {
		return nil
	}
	items, err := s.store.GetWatchlist(ctx)
	if err != nil {
		return fmt.Errorf("load watchlist: %w", err)
	}
	set := make(map[string]map[string]struct{}, len(items))
	for _, it := range items {
		venues := make(map[string]struct{}, len(it.Venues))
		for _, v := range it.Venues {
			venues[v] = struct{}{}
		}
		set[it.Canonical] = venues // 空 venues map = 全部源
	}
	s.watchlistSet = set
	s.watchlistInitOnce = true
	return nil
}

// watchlistHitQuote RLock 命中检查（ns 级，case 1 热路径用，返修包① R1 双维）。
// 未初始化缓存 → 返回 false（调用方应在进 select 前调 ensureWatchlistLoaded）。
// 命中条件：symbol∈set **且**（该项 venues 空 **或** q.VenueKey()∈venues）。
func (s *Service) watchlistHitQuote(q bus.Quote) bool {
	s.watchlistMu.RLock()
	defer s.watchlistMu.RUnlock()
	if !s.watchlistInitOnce {
		return false
	}
	venues, ok := s.watchlistSet[q.Symbol]
	if !ok {
		return false
	}
	if len(venues) == 0 {
		return true // 空 = 全部源
	}
	_, hit := venues[q.VenueKey()]
	return hit
}

// GetWatchlist handler（D-090 §3 + 返修包① R1）：惰性加载 → 返回有序 items（顺序每次查库）。
func (s *Service) GetWatchlist(ctx context.Context, req *connect.Request[v1.GetWatchlistRequest]) (*connect.Response[v1.GetWatchlistResponse], error) {
	if err := s.ensureWatchlistLoaded(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items, err := s.store.GetWatchlist(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.GetWatchlistResponse{Items: itemsToProto(items)}), nil
}

// SetWatchlist handler（D-090 §3 + 返修包① R1）：全程持写锁（校验+写库+更新缓存串行化）。
// 校验每个 canonical ∈ 既有全集（复用 s.canonicals，与矩阵同源）→ 未知者整单拒绝
// （FailedPrecondition + 未知清单进 message，fail-closed 不猜）→ 写库 → 同锁内更新缓存 → 回显。
// venues 不强校验（设计 §9 裁定：动态集合+后果无害）。空清单=合法（清空自选）。
func (s *Service) SetWatchlist(ctx context.Context, req *connect.Request[v1.SetWatchlistRequest]) (*connect.Response[v1.SetWatchlistResponse], error) {
	s.watchlistMu.Lock()
	defer s.watchlistMu.Unlock()

	items := itemsFromProto(req.Msg.GetItems())

	// 空清单=清空，跳过校验直接写库
	if len(items) > 0 {
		known, err := s.canonicals(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load canonicals: %w", err))
		}
		knownSet := make(map[string]struct{}, len(known))
		for _, c := range known {
			knownSet[c] = struct{}{}
		}
		var unknown []string
		for _, it := range items {
			if _, ok := knownSet[it.Canonical]; !ok {
				unknown = append(unknown, it.Canonical)
			}
		}
		if len(unknown) > 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("unknown canonical(s): %s", strings.Join(unknown, ", ")))
		}
	}

	if err := s.store.SetWatchlist(ctx, items); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// 同锁内更新缓存（per-item venue 集合）
	set := make(map[string]map[string]struct{}, len(items))
	for _, it := range items {
		venues := make(map[string]struct{}, len(it.Venues))
		for _, v := range it.Venues {
			venues[v] = struct{}{}
		}
		set[it.Canonical] = venues
	}
	s.watchlistSet = set
	s.watchlistInitOnce = true

	s.kickConfig() // D-469b S5
	return connect.NewResponse(&v1.SetWatchlistResponse{Items: itemsToProto(items)}), nil
}

// itemsToProto pgstore.WatchlistItem → proto WatchlistItem（venues nil/空 → proto 空 repeated）。
func itemsToProto(items []pgstore.WatchlistItem) []*v1.WatchlistItem {
	out := make([]*v1.WatchlistItem, 0, len(items))
	for _, it := range items {
		out = append(out, &v1.WatchlistItem{Canonical: it.Canonical, Venues: it.Venues})
	}
	return out
}

// itemsFromProto proto WatchlistItem → pgstore.WatchlistItem。
func itemsFromProto(pb []*v1.WatchlistItem) []pgstore.WatchlistItem {
	out := make([]pgstore.WatchlistItem, 0, len(pb))
	for _, it := range pb {
		out = append(out, pgstore.WatchlistItem{Canonical: it.GetCanonical(), Venues: it.GetVenues()})
	}
	return out
}
