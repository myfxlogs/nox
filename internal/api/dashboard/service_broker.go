package dashboard

import (
	"context"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/venue"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"

	v1 "nox/internal/gen/dashboardv1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// --- Broker 实持仓（D-051 WP3：MT5/MT4 实际持仓统一视图，实时查不落库） ---

// ListBrokerPositions 返回 Broker 实持仓视图（D-052 monitor 单一真相源）。
// 主源=StreamUpdates 逐仓事件流（D-571 包A 事件归约态）；refresh=true 仅人工调试后门
// （force 重拉一次，D-094：非数据主源，禁当周期刷新用）。
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
	// D-047⑤：探针仓标注（关联仓 type='PROBE' → decision_ref+auto_close_at 透传）
	annotateProbeMeta(ctx, s.store, out)
	// F3：标注账号别名（venue→alias map，未命中留空前端回落 venue）
	annotateAccountAlias(ctx, s.store, out)
	// D-152 微件①：CEX 腿按 (position_id, venue) 聚合 cashflows 填 swap（MT 不动）
	annotateCEXSwap(ctx, s.store, out)
	// D-009 S3：平仓净估（fresh 报价 + cs/fx/费率齐才落值，缺一缺省）
	s.annotateCloseEstimate(ctx, out)
	resp := &v1.ListBrokerPositionsResponse{Positions: out}
	resp.PnlSumUsd, resp.NetSumUsd = brokerSums(out) // D-508 S8：统计条聚合后端下发
	return connect.NewResponse(resp), nil
}

// brokerSums D-508 S8：统计条 Σ pnl / Σ(pnl+swap+commission)（RPC 与流推送同口径；
// 全零/无持仓=nil 缺省 → 前端「—」诚实降级不冒充 0）。
// D-094：样本行（IsSample=position_id 0 未关联系统仓，如手动收息单）不入 Σ——
// #509 卡与面板 Σ 对不上病灶之一；行本身仍显示（前端标「样本」）。
func brokerSums(out []*v1.BrokerPosition) (pnlSum, netSum *float64) {
	var ps, ns float64
	var hasData bool
	for _, p := range out {
		if p.IsSample {
			continue
		}
		ps += p.Pnl
		ns += p.Pnl + p.Swap + p.Commission
		if p.Pnl != 0 || p.Swap != 0 || p.Commission != 0 {
			hasData = true
		}
	}
	if !hasData {
		return nil, nil
	}
	return &ps, &ns
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
	// D-121：也查 LEGGED（CEX 单腿成交的持仓需要 broker position 匹配浮盈）。
	// D-141 核查项：扩 OPENING/CLOSING——过渡态腿的 PositionId 不标注会被误当游离/外单
	// （双计风险：仓级 close_now_net 与游离腿 close_now_estimate 同计）。ACTIVE 单查覆盖四态。
	allPositions, err := store.ListPositions(ctx, "ACTIVE", 500, 0)
	if err != nil {
		return posIdx, orderIdx
	}
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
		bp.IsSample = bp.PositionId == 0 // D-094：孤儿/收息样本行标注（Σ 排除，行仍显示）
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

// annotateCEXSwap D-008 S1：CEX 腿 swap=venue 原生 income_events 窗口合计
// （venue_key+symbol+ts≥openedAt 机械过滤，非估值非系统归因）。
// MT 腿不动（Order.Swap 原生透传）；venue 实报≠0（gate pnl_fund）不动——真值优先。
// 不再绑 position_id：外单（PositionId=0）同享 venue income 源，不再空白（D-008 原则破口实证）。
// 同 symbol 系统仓+外单并存=income 账户级合计（venue 粒度=symbol，已是最细真值）。
// OpenedAt 缺席→不标注（窗口无界会错价，诚实空态）；查询失败不阻断（fail-open，保留原 swap）。
func annotateCEXSwap(ctx context.Context, store *pgstore.Store, bps []*v1.BrokerPosition) {
	if len(bps) == 0 || store == nil {
		return
	}
	now := time.Now()
	for _, bp := range bps {
		if !cexSwapCandidate(bp) {
			continue
		}
		sum, rows, err := store.SumIncomeWindow(ctx, bp.VenueKey, bp.Symbol, bp.OpenedAt.AsTime(), now)
		if err != nil {
			continue // fail-open：单腿查询失败不挡整表（保 venue 原值 0=诚实空态）
		}
		if rows > 0 {
			bp.Swap = sum
		}
	}
}

// cexSwapCandidate 纯函数：CEX 仓且 venue 未报 swap（实报≠0 不覆写）且有开仓时刻（窗口可界）。
func cexSwapCandidate(bp *v1.BrokerPosition) bool {
	return isCEXVenueKey(bp.VenueKey) && bp.Swap == 0 && bp.OpenedAt != nil
}

// AnnotateBrokerPositions D-571 包A：monitor 事件载荷标注入口（四件套同序——
// position_id/order_id → alias → CEX swap → D-009 平仓净估；与 case 8 流推送/拉路径同源口径）。
// main 装配 bpMon.SetAnnotator 用；导出仅因调用方在 cmd 包。svc=nil 时仅前三件（净估跳过）。
func AnnotateBrokerPositions(ctx context.Context, svc *Service, bps []*v1.BrokerPosition) {
	var store *pgstore.Store
	if svc != nil {
		store = svc.store
	}
	annotatePositionID(ctx, store, bps)
	annotateProbeMeta(ctx, store, bps) // D-047⑤：探针标注同源（RPC 与事件路径一致）
	annotateAccountAlias(ctx, store, bps)
	annotateCEXSwap(ctx, store, bps)
	if svc != nil {
		svc.annotateCloseEstimate(ctx, bps)
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
func brokerPositionToProto(o venue.Order, canonical string, price bus.Quote, hasPrice bool, venueOnline bool, qbus *bus.QuoteBus, freshTTL time.Duration, now time.Time, margin float64, swapChargeAt *timestamppb.Timestamp, fundingRatePeriodicPct float64) *v1.BrokerPosition {
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
		IsArb:       exec.IsOrderComment(o.Comment),
		Magic:       o.Magic,
		VenueOnline: venueOnline,
		Margin:      margin,     // D-124：方案 C 分摊保证金（0=未知/CEX/无账户数据）
		Sl:          o.StopLoss, // D-155：broker 端 SL 透传（MT5/MT4 Order.StopLoss；0=未设/CEX）
	}
	// D-162 ②：有价即照填（stale 价也填——休市末价比「—」诚实），Fresh 按
	// quoteFreshForDisplay 如实判；price_at=报价 tick 真钟供前端标时点。
	// est/退出闸等判定径吃 Fresh=false 天然不误用 stale 值（est.go:31 门控）。
	if hasPrice {
		// D-009 S2：现价=平仓方向价（BUY仓平吃 bid / SELL仓平吃 ask）；双值齐填。
		pb.PriceBid, pb.PriceAsk = price.Bid, price.Ask
		pb.CurrentPrice = price.Bid
		if o.Side == "SELL" {
			pb.CurrentPrice = price.Ask
		}
		pb.Fresh = quoteFreshForDisplay(qbus, price, freshTTL, now) // D-155：脉冲续命分档
		pb.PriceAt = timestamppb.New(price.Time)
	}
	if !o.OpenedAt.IsZero() {
		pb.OpenedAt = timestamppb.New(o.OpenedAt)
	}
	pb.SwapChargeAt = swapChargeAt                     // D-337：下次计费/结算时刻（同 venue 同值；nil=时区/周期未知诚实空态）
	pb.FundingRatePeriodicPct = fundingRatePeriodicPct // D-337R1：CEX 周期费率%（MT5=0 空态）
	return pb
}
