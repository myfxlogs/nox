package dashboard

// service_pnl_anchor.go 盈亏统计起点 + PnL 总览聚合下发（F4 anchor + D-508 S8 前端零信任收编；自 service_positions.go 拆出）。

import (
	"context"

	"connectrpc.com/connect"

	v1 "arb/internal/gen/dashboardv1"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- 盈亏统计起点（F4：anchor 重置，业主选 A「不删历史」） ---

// GetPnlAnchor 返回盈亏统计起点 + 累计已实现（closed_at >= anchor 后端口径）。
// 前端「累计已实现」改用此后端口径（替代前端全量聚合，支持 anchor 重置）。
// 「今日已实现」不受 anchor 影响（当日口径，前端聚合不变）。
func (s *Service) GetPnlAnchor(ctx context.Context, req *connect.Request[v1.GetPnlAnchorRequest]) (*connect.Response[v1.GetPnlAnchorResponse], error) {
	anchor, err := s.store.GetPnlAnchor(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	realized, err := s.store.RealizedPnlSinceAnchor(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	resp := &v1.GetPnlAnchorResponse{
		Anchor:              timestamppb.New(anchor),
		RealizedSinceAnchor: realized,
	}
	// D-508 S8：今日已实现 + 浮动聚合后端下发（前端零聚合——pnl.ts 聚合函数退役）。
	if today, err := s.store.RealizedPnlToday(ctx); err == nil {
		resp.RealizedToday = today
	}
	if s.bpm != nil {
		bpos := s.bpm.Snapshot()
		var fsum float64
		for _, p := range bpos {
			fsum += p.Pnl + p.Swap + p.Commission
		}
		resp.FloatingNetUsd = fsum
		resp.FloatingAvailable = len(bpos) > 0 || s.anyAccountOnline()
	}
	return connect.NewResponse(resp), nil
}

// anyAccountOnline 任一账户在线（D-508 S8：浮动「—」降级判据——全离线/无账户=不可信 0）。
func (s *Service) anyAccountOnline() bool {
	if s.acctMon == nil {
		return false
	}
	for _, info := range s.acctMon.Snapshot() {
		if info != nil && info.Online {
			return true
		}
	}
	return false
}

// ResetPnlAnchor 重置盈亏统计起点为 now()（业主点「清零」→ anchor=now()）。
// 历史 CLOSED 持仓不删除（业主选 A「不删历史」）；今日口径与停机状态不变。
func (s *Service) ResetPnlAnchor(ctx context.Context, req *connect.Request[v1.ResetPnlAnchorRequest]) (*connect.Response[v1.ResetPnlAnchorResponse], error) {
	anchor, err := s.store.ResetPnlAnchor(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.ResetPnlAnchorResponse{
		Anchor: timestamppb.New(anchor),
	}), nil
}
