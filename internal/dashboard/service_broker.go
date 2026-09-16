package dashboard

import (
	"context"
	"fmt"
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
	// D-152 微件①：CEX 腿按 (position_id, venue) 聚合 cashflows 填 swap（MT 不动）
	annotateCEXSwap(ctx, s.store, out)
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
// D-121：CEX perp 聚合仓位无单一 ticket → 额外按 venueKey+symbol 索引（CEXPositionQuerier Ticket=Symbol）。
// S4（D-146）：同时构造 (venueKey, ticket)→order_id 索引——CEX 聚合仓位 Ticket=Symbol，
// order_id 才是真实 CEX orderId（MT 模式下 order_id==ticket，透出无害）。
// 查询失败返回空索引（fail-open，调用方自行处理）。
func buildPositionIDIndex(ctx context.Context, store PositionLegLister) (map[string]int64, map[string]string) {
	posIdx := make(map[string]int64)
	orderIdx := make(map[string]string)
	if store == nil {
		return posIdx, orderIdx
	}
	openPositions, err := store.ListPositions(ctx, "OPEN", 200, 0)
	if err != nil {
		return posIdx, orderIdx
	}
	// D-121：也查 LEGGED（CEX 单腿成交的持仓需要 broker position 匹配浮盈）
	leggedPositions, _ := store.ListPositions(ctx, "LEGGED", 200, 0)
	allPositions := append(openPositions, leggedPositions...)
	for _, p := range allPositions {
		legs, err := store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			continue
		}
		for _, l := range legs {
			if l.Phase == "OPEN" && l.Status == "FILLED" {
				if l.OrderID != "" {
					posIdx[l.Venue+"\x00"+l.OrderID] = p.ID
					orderIdx[l.Venue+"\x00"+l.OrderID] = l.OrderID
				}
				// D-121：CEX 聚合仓位按 venueKey+symbol 匹配（Ticket=Symbol）
				if l.Symbol != "" {
					posIdx[l.Venue+"\x00"+l.Symbol] = p.ID
					orderIdx[l.Venue+"\x00"+l.Symbol] = l.OrderID
				}
			}
		}
	}
	return posIdx, orderIdx
}

// annotatePositionID 用 OPEN position legs 构造 (venueKey, ticket)→position_id + order_id 索引，标注 broker 持仓。
// 匹配口径唯一在此（P3）：leg.Venue == bp.VenueKey && leg.OrderID == bp.Ticket。
// S4（D-146）：同时标注 order_id（CEX 聚合仓位 Ticket=Symbol 时 order_id 才是真实 orderId）。
// 查询失败不阻断（position_id 全 0 / order_id 全空，前端显示「未关联」）——fail-open，视图可用优先。
func annotatePositionID(ctx context.Context, store *pgstore.Store, bps []*v1.BrokerPosition) {
	if len(bps) == 0 || store == nil {
		return
	}
	posIdx, orderIdx := buildPositionIDIndex(ctx, store)
	annotateWithIndex(bps, posIdx, orderIdx)
}

// annotateWithIndex 纯函数：用预建索引标注 broker 持仓 position_id + order_id（可单测，无 DB 依赖）。
// D-311 R7④：PositionId 命中即标 IsArb——CEX 聚合仓无 comment（MT comment 前缀判定保留为
// 初值），positionId 标注是 CEX 腿唯一归属信号。
func annotateWithIndex(bps []*v1.BrokerPosition, posIdx map[string]int64, orderIdx map[string]string) {
	for _, bp := range bps {
		key := bp.VenueKey + "\x00" + bp.Ticket
		bp.PositionId = posIdx[key]
		bp.OrderId = orderIdx[key]
		if bp.PositionId != 0 {
			bp.IsArb = true
		}
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

// annotateCEXSwap D-152 微件①：CEX 腿按 (position_id, venue) 聚合 position_cashflows 填 swap 字段。
// MT 腿不动（Order.Swap 透传链已完整）。仅在 annotatePositionID 之后调用（需 position_id）。
// 查询失败不阻断（fail-open，保留原 swap）。无 cashflow 行的 CEX 仓保留原 swap（Gate funding / 0）。
func annotateCEXSwap(ctx context.Context, store *pgstore.Store, bps []*v1.BrokerPosition) {
	if len(bps) == 0 || store == nil {
		return
	}
	// 收集 CEX 仓的 position_id（去重）
	seen := make(map[int64]bool)
	var posIDs []int64
	for _, bp := range bps {
		if bp.PositionId > 0 && isCEXVenueKey(bp.VenueKey) && !seen[bp.PositionId] {
			seen[bp.PositionId] = true
			posIDs = append(posIDs, bp.PositionId)
		}
	}
	if len(posIDs) == 0 {
		return
	}
	sums, err := store.SumCashflowsByPosVenue(ctx, posIDs)
	if err != nil {
		return // fail-open
	}
	annotateCEXSwapWithMap(bps, sums)
}

// annotateCEXSwapWithMap 纯函数：用预建 (posID\x00venue)→sum map 标注 CEX 仓 swap（可单测）。
func annotateCEXSwapWithMap(bps []*v1.BrokerPosition, sums map[string]float64) {
	for _, bp := range bps {
		if bp.PositionId > 0 && isCEXVenueKey(bp.VenueKey) {
			key := fmt.Sprintf("%d\x00%s", bp.PositionId, bp.VenueKey)
			if sum, ok := sums[key]; ok {
				bp.Swap = sum
			}
		}
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
// pnl：venue 权威浮盈（Order.Profit 透传，D-043 零信任）——MT5 已换算账户货币（USD），不重算。
//
//	~~F5 实时重算~~ 已移除（D-116）：原公式 qty×(mid−open)×dir 缺 ContractSize+fx 换算，
//	JPY 计价符号差 ~660 倍。venue 每次 poll 拿最新 Profit（已实时），无需重算。
//
// venueOnline=false = 离线 venue 的最近快照（非实时，诚实标注，D-052）。
func brokerPositionToProto(o reconcile.VenueOrder, canonical string, price bus.Quote, hasPrice bool, venueOnline bool, freshTTL time.Duration, now time.Time, margin float64) *v1.BrokerPosition {
	pb := &v1.BrokerPosition{
		Venue:       o.Venue,
		VenueKey:    o.VenueKey,
		Ticket:      o.Ticket,
		Symbol:      o.Symbol,
		Canonical:   canonical,
		Side:        o.Side,
		Volume:      o.Volume,
		OpenPrice:   o.OpenPrice,
		Pnl:         o.Profit, // venue 权威浮盈（D-043 零信任，不重算）
		Swap:        o.Swap,
		Commission:  o.Commission,
		Comment:     o.Comment,
		IsArb:       strings.HasPrefix(o.Comment, exec.OrderComment),
		Magic:       o.Magic,
		VenueOnline: venueOnline,
		Margin:      margin,     // D-124：方案 C 分摊保证金（0=未知/CEX/无账户数据）
		Sl:          o.StopLoss, // D-155：broker 端 SL 透传（MT5/MT4 Order.StopLoss；0=未设/CEX）
	}
	if hasPrice && now.Sub(price.Time) <= freshTTL {
		pb.CurrentPrice = (price.Bid + price.Ask) / 2
		pb.Fresh = true
	}
	if !o.OpenedAt.IsZero() {
		pb.OpenedAt = timestamppb.New(o.OpenedAt)
	}
	return pb
}
