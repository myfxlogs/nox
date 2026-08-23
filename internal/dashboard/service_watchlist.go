package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

// SetQuoteFlushInterval 注入 StreamUpdates case 1 蓄压 flush 间隔（D-090）。
// main 装配后调；缺省 1s（看盘条足够，无信息损失——最新值语义）；测试注 10ms。
func (s *Service) SetQuoteFlushInterval(d time.Duration) { s.quoteFlushInterval = d }

// ensureWatchlistLoaded 惰性加载 watchlist 缓存（D-090 §4）。
// 首次调用查库填 watchlistSet + 置 watchlistInitOnce=true（空表也置，避免重复查）。
// 全程持写锁（与 SetWatchlist 互斥）。
func (s *Service) ensureWatchlistLoaded(ctx context.Context) error {
	s.watchlistMu.Lock()
	defer s.watchlistMu.Unlock()
	if s.watchlistInitOnce {
		return nil
	}
	list, err := s.store.GetWatchlist(ctx)
	if err != nil {
		return fmt.Errorf("load watchlist: %w", err)
	}
	set := make(map[string]struct{}, len(list))
	for _, c := range list {
		set[c] = struct{}{}
	}
	s.watchlistSet = set
	s.watchlistInitOnce = true
	return nil
}

// watchlistHit RLock 命中检查（ns 级，case 1 热路径用）。
// 未初始化缓存 → 返回 false（调用方应在进 select 前调 ensureWatchlistLoaded）。
func (s *Service) watchlistHit(canonical string) bool {
	s.watchlistMu.RLock()
	defer s.watchlistMu.RUnlock()
	if !s.watchlistInitOnce {
		return false
	}
	_, ok := s.watchlistSet[canonical]
	return ok
}

// GetWatchlist handler（D-090 §3）：惰性加载 → 返回有序列表（顺序每次查库，缓存只存 set）。
func (s *Service) GetWatchlist(ctx context.Context, req *connect.Request[v1.GetWatchlistRequest]) (*connect.Response[v1.GetWatchlistResponse], error) {
	if err := s.ensureWatchlistLoaded(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	list, err := s.store.GetWatchlist(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.GetWatchlistResponse{Canonical: list}), nil
}

// SetWatchlist handler（D-090 §3）：全程持写锁（校验+写库+更新缓存串行化）。
// 校验每个 canonical ∈ 既有全集（复用 s.canonicals，与矩阵同源）→ 未知者整单拒绝
// （FailedPrecondition + 未知清单进 message，fail-closed 不猜）→ 写库 → 同锁内更新缓存 → 回显。
// 空清单=合法（清空自选）。
func (s *Service) SetWatchlist(ctx context.Context, req *connect.Request[v1.SetWatchlistRequest]) (*connect.Response[v1.SetWatchlistResponse], error) {
	s.watchlistMu.Lock()
	defer s.watchlistMu.Unlock()

	canonicals := req.Msg.GetCanonical()

	// 空清单=清空，跳过校验直接写库
	if len(canonicals) > 0 {
		known, err := s.canonicals(ctx)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("load canonicals: %w", err))
		}
		knownSet := make(map[string]struct{}, len(known))
		for _, c := range known {
			knownSet[c] = struct{}{}
		}
		var unknown []string
		for _, c := range canonicals {
			if _, ok := knownSet[c]; !ok {
				unknown = append(unknown, c)
			}
		}
		if len(unknown) > 0 {
			return nil, connect.NewError(connect.CodeFailedPrecondition,
				fmt.Errorf("unknown canonical(s): %s", strings.Join(unknown, ", ")))
		}
	}

	if err := s.store.SetWatchlist(ctx, canonicals); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// 同锁内更新缓存
	set := make(map[string]struct{}, len(canonicals))
	for _, c := range canonicals {
		set[c] = struct{}{}
	}
	s.watchlistSet = set
	s.watchlistInitOnce = true

	return connect.NewResponse(&v1.SetWatchlistResponse{Canonical: canonicals}), nil
}
