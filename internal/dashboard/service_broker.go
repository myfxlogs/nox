package dashboard

import (
	"context"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Broker 实持仓（D-051 WP3：MT5/MT4 实际持仓统一视图，实时查不落库） ---

// ListBrokerPositions 返回 Broker 实持仓视图（D-052 monitor 单一真相源）。
// refresh=true 强制触发一次 venue 轮询后返回（前端「刷新」/平仓后立即刷新）；否则返回最近缓存（≤ 轮询周期）。
// canonical 解析 + 现价计算均在后端（D-043 零信任：前端只渲染）；离线 venue 保留最近快照并 venue_online=false。
// S4（D-073+）：返回前用 OPEN legs 的 (venueKey, order_id)→position_id 索引标注 position_id（0=未关联孤儿订单）。
func (s *Service) ListBrokerPositions(ctx context.Context, req *connect.Request[v1.ListBrokerPositionsRequest]) (*connect.Response[v1.ListBrokerPositionsResponse], error) {
	if s.bpm == nil {
		return connect.NewResponse(&v1.ListBrokerPositionsResponse{}), nil
	}
	var out []*v1.BrokerPosition
	if req.Msg.GetRefresh() {
		out = s.bpm.Refresh(ctx)
	} else {
		out = s.bpm.Snapshot()
	}
	// S4：标注 position_id（OPEN legs venue+order_id → position_id 索引）
	annotatePositionID(ctx, s.store, out)
	// F3：标注账号别名（venue→alias map，未命中留空前端回落 venue）
	annotateAccountAlias(ctx, s.store, out)
	return connect.NewResponse(&v1.ListBrokerPositionsResponse{Positions: out}), nil
}

// PositionLegLister 查询 OPEN positions + legs 的接口（buildPositionIDIndex 依赖）。
// pgstore.Store 实现此接口；测试可注入 fake。
type PositionLegLister interface {
	ListPositions(ctx context.Context, status string, limit, offset int) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
}

// buildPositionIDIndex 构造 (venueKey, ticket)→position_id 索引（D-110 P2 A1 抽取）。
// 查 OPEN positions → 遍历 OPEN legs → leg.Venue+"\x00"+leg.OrderID → posID。
// 查询失败返回空索引（fail-open，调用方自行处理）。
func buildPositionIDIndex(ctx context.Context, store PositionLegLister) map[string]int64 {
	idx := make(map[string]int64)
	if store == nil {
		return idx
	}
	openPositions, err := store.ListPositions(ctx, "OPEN", 200, 0)
	if err != nil {
		return idx
	}
	for _, p := range openPositions {
		legs, err := store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, l := range legs {
			if l.Phase == "OPEN" && l.OrderID != "" {
				idx[l.Venue+"\x00"+l.OrderID] = p.ID
			}
		}
	}
	return idx
}

// annotatePositionID 用 OPEN position legs 构造 (venueKey, ticket)→position_id 索引，标注 broker 持仓。
// 匹配口径唯一在此（P3）：leg.Venue == bp.VenueKey && leg.OrderID == bp.Ticket。
// 查询失败不阻断（position_id 全 0，前端显示「未关联」）——fail-open，视图可用优先。
func annotatePositionID(ctx context.Context, store *pgstore.Store, bps []*v1.BrokerPosition) {
	if len(bps) == 0 || store == nil {
		return
	}
	idx := buildPositionIDIndex(ctx, store)
	annotateWithIndex(bps, idx)
}

// annotateWithIndex 纯函数：用预建索引标注 broker 持仓 position_id（可单测，无 DB 依赖）。
func annotateWithIndex(bps []*v1.BrokerPosition, idx map[string]int64) {
	for _, bp := range bps {
		bp.PositionId = idx[bp.VenueKey+"\x00"+bp.Ticket]
	}
}

// annotateAccountAlias F3：用 venue→alias map 标注 broker 持仓账号别名（未命中留空，前端回落 venue）。
// 别名更新生效时延=accounts 快照周期，可接受，勿为此加事件。查询失败不阻断（fail-open）。
func annotateAccountAlias(ctx context.Context, store *pgstore.Store, bps []*v1.BrokerPosition) {
	if len(bps) == 0 || store == nil {
		return
	}
	m, err := store.ListAccountAliases(ctx)
	if err != nil {
		return
	}
	annotateAliasWithMap(bps, m)
}

// annotateAliasWithMap 纯函数：用预建 venue→alias map 标注（可单测，无 DB 依赖）。
func annotateAliasWithMap(bps []*v1.BrokerPosition, m map[string]string) {
	for _, bp := range bps {
		bp.AccountAlias = m[bp.Venue]
	}
}

// CloseBrokerPosition 经注册执行器平仓一条 venue 持仓（人工确认后，§1）。
func (s *Service) CloseBrokerPosition(ctx context.Context, req *connect.Request[v1.CloseBrokerPositionRequest]) (*connect.Response[v1.CloseBrokerPositionResponse], error) {
	if _, err := s.bm.CloseOrder(ctx, req.Msg.GetVenueKey(), req.Msg.GetTicket()); err != nil {
		return connect.NewResponse(&v1.CloseBrokerPositionResponse{Status: "FAILED", Message: err.Error()}), nil
	}
	return connect.NewResponse(&v1.CloseBrokerPositionResponse{Status: "CLOSED"}), nil
}

// CloseBrokerPositions 批量平仓（D-052 全平选中）：逐条执行，单条失败不阻断后续。
// 前端二次点击确认后调用；每项独立返回 CLOSED/FAILED。
func (s *Service) CloseBrokerPositions(ctx context.Context, req *connect.Request[v1.CloseBrokerPositionsRequest]) (*connect.Response[v1.CloseBrokerPositionsResponse], error) {
	items := req.Msg.GetItems()
	results := make([]*v1.CloseBrokerPositionResponse, 0, len(items))
	for _, it := range items {
		if _, err := s.bm.CloseOrder(ctx, it.GetVenueKey(), it.GetTicket()); err != nil {
			results = append(results, &v1.CloseBrokerPositionResponse{Status: "FAILED", Message: err.Error()})
			continue
		}
		results = append(results, &v1.CloseBrokerPositionResponse{Status: "CLOSED"})
	}
	return connect.NewResponse(&v1.CloseBrokerPositionsResponse{Results: results}), nil
}

// --- 账户资产（D-053） ---

// ListAccounts 返回账户资产快照（acctMon 单一真相源；≤ 轮询周期的缓存，无需强制刷新）。
func (s *Service) ListAccounts(ctx context.Context, req *connect.Request[v1.ListAccountsRequest]) (*connect.Response[v1.ListAccountsResponse], error) {
	if s.acctMon == nil {
		return connect.NewResponse(&v1.ListAccountsResponse{}), nil
	}
	return connect.NewResponse(&v1.ListAccountsResponse{Accounts: s.acctMon.Snapshot()}), nil
}

// brokerPositionToProto 组装单个 venue 持仓响应。
// current_price：canonical 已解析且报价帧新鲜（now.Sub ≤ freshTTL）→ mid，fresh=true；否则 0/false（诚实空态）。
// is_arb：comment 前缀 = exec.OrderComment（复用 reconcile.isARBOrder 语义，D-032）。
// pnl：F5 实时化——有新鲜报价时用 qbus 重算（qty × (现价 − 开仓价) × 方向系数）覆盖 venue 透传值；
//
//	无报价/陈旧 → 保持 venue 权威浮盈（Order.Profit 透传，D-043）+ fresh=false 诚实降级。
//
// venueOnline=false = 离线 venue 的最近快照（非实时，诚实标注，D-052）。
func brokerPositionToProto(o reconcile.VenueOrder, canonical string, price bus.Quote, hasPrice bool, venueOnline bool, freshTTL time.Duration, now time.Time) *v1.BrokerPosition {
	pb := &v1.BrokerPosition{
		Venue:       o.Venue,
		VenueKey:    o.VenueKey,
		Ticket:      o.Ticket,
		Symbol:      o.Symbol,
		Canonical:   canonical,
		Side:        o.Side,
		Volume:      o.Volume,
		OpenPrice:   o.OpenPrice,
		Pnl:         o.Profit, // 默认 venue 透传，新鲜报价时下方覆盖
		Swap:        o.Swap,
		Commission:  o.Commission,
		Comment:     o.Comment,
		IsArb:       strings.HasPrefix(o.Comment, exec.OrderComment),
		Magic:       o.Magic,
		VenueOnline: venueOnline,
	}
	if hasPrice && now.Sub(price.Time) <= freshTTL {
		mid := (price.Bid + price.Ask) / 2
		pb.CurrentPrice = mid
		pb.Fresh = true
		// F5：新鲜报价 → 实时重算浮盈（BUY: +qty×(mid−open)；SELL: +qty×(open−mid)）
		dir := 1.0
		if o.Side == "SELL" {
			dir = -1.0
		}
		pb.Pnl = o.Volume * (mid - o.OpenPrice) * dir
	}
	if !o.OpenedAt.IsZero() {
		pb.OpenedAt = timestamppb.New(o.OpenedAt)
	}
	return pb
}
