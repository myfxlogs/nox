// Package reconcile 实现持仓对账（§13.4 B2）。
// 查 venue 实际持仓与本地 OPEN legs 比对，不一致 → critical 告警。
package reconcile

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	"arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
	"google.golang.org/grpc/metadata"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// VenueQuerier 查询 venue 实际持仓的统一接口。
// VenueKey 提供查询器身份（"{venue}:{type}"）：离线跟踪需要 0 持仓成功时也知道 venue（D-052）。
type VenueQuerier interface {
	QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error)
	VenueKey() string // "{venue}:{venueType}"
}

// Store 对账器所需的 store 方法子集（D-073+ 接口化，便于单测 fakeStore）。
// pgstore.Store 实现该接口（结构性 typing，无需显式声明）。
type Store interface {
	ListPositions(ctx context.Context, status string, limit, offset int) ([]pgstore.Position, error)
	ListPositionLegs(ctx context.Context, posID int64) ([]pgstore.PositionLeg, error)
	CreatePositionLeg(ctx context.Context, leg *pgstore.PositionLeg) error
	ClosePosition(ctx context.Context, id int64, realizedPnl float64) error
	UpdatePositionStatus(ctx context.Context, id int64, status string) error
	InsertAlert(ctx context.Context, posID *int64, level, msg string) error
}

// VenueOrder venue 侧的持仓记录。
// Comment 用于判别订单归属：ARB 源订单（exec.OrderComment 前缀）vs 系统外单。
type VenueOrder struct {
	Ticket    string
	Symbol    string
	Side      string // BUY / SELL
	Volume    float64
	OpenPrice float64
	Venue     string
	Comment   string

	// WP3（D-051）加性扩展：对外视图（ListBrokerPositions）需要的权威字段，对账器原有逻辑不动。
	VenueKey   string  // "{venue}:{venueType}"（query 时已知类型）
	Profit     float64 // venue 权威浮盈（Order.Profit）
	Swap       float64
	Commission float64
	OpenedAt   time.Time // 开仓时间（零值 = venue 未返回）
	Magic      int64     // MT5 ExpertId / MT4 MagicNumber（0 = 无，D-052）
}

// AccountInfo 一个账户的资金视图（D-053）。CFD 来自 MT AccountSummary；CEX 来自签名余额查询。
// CEX 的 Margin/FreeMargin/Credit/Profit = 0（无对应概念，前端按 AssetType=cex 置「—」）。
type AccountInfo struct {
	VenueKey   string // "{venue}:{venueType}"
	Venue      string
	VenueType  string // mt5 / mt4 / spot
	AssetType  string // cfd / cex
	Balance    float64
	Equity     float64
	Margin     float64
	FreeMargin float64
	Credit     float64
	Profit     float64
	Leverage   float64
	Currency   string
}

// AccountQuerier 查询单个账户资金的统一接口（D-053）。与 VenueQuerier 并列，同用 VenueKey 身份。
// 查询失败 → online=false（monitor 保留最近快照，复用 D-052 离线语义）。
type AccountQuerier interface {
	Account(ctx context.Context) (*AccountInfo, error)
	VenueKey() string
}

// accountSummaryToInfo 从 MT5 AccountSummary 映射账户视图（D-053）。
func accountSummaryToInfoMT5(venue, venueType string, sum *mt5pb.AccountSummary) *AccountInfo {
	return &AccountInfo{
		VenueKey:   venue + ":" + venueType,
		Venue:      venue,
		VenueType:  venueType,
		AssetType:  "cfd",
		Balance:    sum.GetBalance(),
		Credit:     sum.GetCredit(),
		Profit:     sum.GetProfit(),
		Equity:     sum.GetEquity(),
		Margin:     sum.GetMargin(),
		FreeMargin: sum.GetFreeMargin(),
		Leverage:   sum.GetLeverage(),
		Currency:   sum.GetCurrency(),
	}
}

// accountSummaryToInfoMT4 从 MT4 AccountSummary 映射账户视图（字段与 MT5 同形，生成类型不同）。
func accountSummaryToInfoMT4(venue, venueType string, sum *mt4.AccountSummary) *AccountInfo {
	return &AccountInfo{
		VenueKey:   venue + ":" + venueType,
		Venue:      venue,
		VenueType:  venueType,
		AssetType:  "cfd",
		Balance:    sum.GetBalance(),
		Credit:     sum.GetCredit(),
		Profit:     sum.GetProfit(),
		Equity:     sum.GetEquity(),
		Margin:     sum.GetMargin(),
		FreeMargin: sum.GetFreeMargin(),
		Leverage:   sum.GetLeverage(),
		Currency:   sum.GetCurrency(),
	}
}

// isARBOrder 判断 broker 订单是否为当前 ARB 系统开出。
// 当前 exec 下单 comment 统一为 exec.OrderComment（"arb"）→ 前缀匹配即 ARB 源。
// 其余一律系统外单：遗留 ca_/cx_/tri_+hex 格式（旧系统 D-033）、手工单 UUID、空 comment。
func isARBOrder(comment string) bool {
	return strings.HasPrefix(comment, exec.OrderComment)
}

// tsToTime nil-safe 转换：nil Timestamp → time.Time{}（开仓时间零值 = venue 未返回）。
func tsToTime(ts *timestamppb.Timestamp) time.Time {
	if ts == nil {
		return time.Time{}
	}
	return ts.AsTime()
}

// MT4Querier 通过 MT4 gRPC 查询持仓。
type MT4Querier struct {
	token string
	mt4   mt4.MT4Client
	venue string
}

func NewMT4Querier(venue, token string, mt4Client mt4.MT4Client) *MT4Querier {
	return &MT4Querier{venue: venue, token: token, mt4: mt4Client}
}

func (q *MT4Querier) VenueKey() string { return q.venue + ":mt4" }

// Account 查 MT4 账户资金（AccountSummary，D-053）。
func (q *MT4Querier) Account(ctx context.Context) (*AccountInfo, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt4.AccountSummary(ctx, &mt4.AccountSummaryRequest{Id: q.token})
	if err != nil {
		return nil, fmt.Errorf("mt4 account summary: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt4 account summary: %s", e.GetMessage())
	}
	return accountSummaryToInfoMT4(q.venue, "mt4", resp.GetResult()), nil
}

func (q *MT4Querier) QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt4.OpenedOrders(ctx, &mt4.OpenedOrdersRequest{Id: q.token})
	if err != nil {
		return nil, fmt.Errorf("mt4 opened orders: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt4 opened orders: %s", e.GetMessage())
	}
	var out []VenueOrder
	for _, ord := range resp.GetResult() {
		side := "BUY"
		if ord.GetType() == mt4.Op_Op_Sell {
			side = "SELL"
		}
		out = append(out, VenueOrder{
			Ticket:     strconv.FormatInt(int64(ord.GetTicket()), 10),
			Symbol:     ord.GetSymbol(),
			Side:       side,
			Volume:     ord.GetLots(),
			OpenPrice:  ord.GetOpenPrice(),
			Venue:      q.venue,
			Comment:    ord.GetComment(),
			VenueKey:   q.venue + ":mt4",
			Profit:     ord.GetProfit(),
			Swap:       ord.GetSwap(),
			Commission: ord.GetCommission(),
			OpenedAt:   tsToTime(ord.GetOpenTime()),
			Magic:      int64(ord.GetMagicNumber()),
		})
	}
	return out, nil
}

// MT5Querier 通过 MT5 gRPC 查询持仓。
type MT5Querier struct {
	token string
	mt5   mt5pb.MT5Client
	venue string
}

func NewMT5Querier(venue, token string, mt5Client mt5pb.MT5Client) *MT5Querier {
	return &MT5Querier{venue: venue, token: token, mt5: mt5Client}
}

func (q *MT5Querier) VenueKey() string { return q.venue + ":mt5" }

// Account 查 MT5 账户资金（AccountSummary，D-053）。
func (q *MT5Querier) Account(ctx context.Context) (*AccountInfo, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt5.AccountSummary(ctx, &mt5pb.AccountSummaryRequest{Id: q.token})
	if err != nil {
		return nil, fmt.Errorf("mt5 account summary: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt5 account summary: %s", e.GetMessage())
	}
	return accountSummaryToInfoMT5(q.venue, "mt5", resp.GetResult()), nil
}

func (q *MT5Querier) QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "id", q.token)
	resp, err := q.mt5.OpenedOrders(ctx, &mt5pb.OpenedOrdersRequest{Id: q.token})
	if err != nil {
		return nil, fmt.Errorf("mt5 opened orders: %w", err)
	}
	if e := resp.GetError(); e != nil {
		return nil, fmt.Errorf("mt5 opened orders: %s", e.GetMessage())
	}
	var out []VenueOrder
	for _, ord := range resp.GetResult() {
		side := "BUY"
		if ord.GetOrderType() == mt5pb.OrderType_OrderType_Sell {
			side = "SELL"
		}
		out = append(out, VenueOrder{
			Ticket:     strconv.FormatInt(ord.GetTicket(), 10),
			Symbol:     ord.GetSymbol(),
			Side:       side,
			Volume:     ord.GetLots(),
			OpenPrice:  ord.GetOpenPrice(),
			Venue:      q.venue,
			Comment:    ord.GetComment(),
			VenueKey:   q.venue + ":mt5",
			Profit:     ord.GetProfit(),
			Swap:       ord.GetSwap(),
			Commission: ord.GetCommission(),
			OpenedAt:   tsToTime(ord.GetOpenTime()),
			Magic:      ord.GetExpertId(),
		})
	}
	return out, nil
}

// Reconciler 对账器：比对本地 OPEN legs 与 venue 实际持仓。
type Reconciler struct {
	store           Store
	mu              sync.RWMutex
	queriers        []VenueQuerier
	historyQueriers []OrderHistoryQuerier
	closer          Closer
	statUpdater     StatUpdater
	runTimeout      time.Duration // 单次外部调用超时（缺陷1修复：默认 10s，防离线 venue 阻塞 loop）
	initialDelay    time.Duration // 首轮延迟（缺陷2修复：默认 60s，等 adapter 就绪）
}

// New 创建对账器。
func New(store Store, queriers ...VenueQuerier) *Reconciler {
	return &Reconciler{
		store:        store,
		queriers:     queriers,
		runTimeout:   10 * time.Second,
		initialDelay: 60 * time.Second,
	}
}

// SetCloser 注入平仓器（对账自动收尾用，D-073+）。main 装配 service 后调。
func (r *Reconciler) SetCloser(c Closer) { r.closer = c }

// SetStatUpdater 注入策略统计更新器（收尾后调，淘汰统计不断链）。
func (r *Reconciler) SetStatUpdater(s StatUpdater) { r.statUpdater = s }

// SetOrderHistoryQueriers 设置历史单查证器集合（与 VenueQuerier 同 venueKey 身份）。
func (r *Reconciler) SetOrderHistoryQueriers(qs ...OrderHistoryQuerier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.historyQueriers = append([]OrderHistoryQuerier(nil), qs...)
}

// SetQueriers 整体替换 querier 集合（Broker 管理器启停适配器时调用）。
func (r *Reconciler) SetQueriers(qs ...VenueQuerier) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.queriers = append([]VenueQuerier(nil), qs...)
}

// snapshot 返回 querier 集合的快照（Run 期间避免对集合的并发读写）。
func (r *Reconciler) snapshot() []VenueQuerier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.queriers
}

// historySnapshot 返回历史单查证器集合快照。
func (r *Reconciler) historySnapshot() []OrderHistoryQuerier {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.historyQueriers
}

// findHistoryQuerier 按 venueKey 找历史单查证器（venueKey = "{venue}:{type}"，与 leg.Venue 同源）。
func (r *Reconciler) findHistoryQuerier(venueKey string) OrderHistoryQuerier {
	for _, q := range r.historySnapshot() {
		if q.VenueKey() == venueKey {
			return q
		}
	}
	return nil
}

// withTimeout 派生带超时的 ctx（缺陷1修复：所有外部调用一律包超时，防离线 venue 阻塞 loop）。
func (r *Reconciler) withTimeout(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, r.runTimeout)
}

// insertAlertCtx 带超时插告警（缺陷1修复：InsertAlert 也包超时）。
func (r *Reconciler) insertAlertCtx(ctx context.Context, level, msg string) {
	sub, cancel := r.withTimeout(ctx)
	defer cancel()
	if err := r.store.InsertAlert(sub, nil, level, msg); err != nil {
		log.Printf("[reconcile] alert: %v", err)
	}
}

// Run 执行一次对账。返回不一致数量。
// 缺陷1修复：所有外部调用（venue querier / OrderHistory / InsertAlert / store 写）一律包 runTimeout 超时，
// 单个 venue 超时不炸整个 Run（该 venue 本轮跳过 + 计数日志）。
// 缺陷2修复：findHistoryQuerier 返回 nil 时不告警不计数（查证器未就绪，下轮复查），仅 debug 日志。
func (r *Reconciler) Run(ctx context.Context) (int, error) {
	// 1. 查本地 OPEN + OPENING 持仓（OPENING 僵尸收编，D-073+）
	listCtx, listCancel := r.withTimeout(ctx)
	openPositions, err := r.store.ListPositions(listCtx, "OPEN", 200, 0)
	listCancel()
	if err != nil {
		return 0, fmt.Errorf("list open positions: %w", err)
	}
	listCtx, listCancel = r.withTimeout(ctx)
	openingPositions, err := r.store.ListPositions(listCtx, "OPENING", 50, 0)
	listCancel()
	if err != nil {
		log.Printf("[reconcile] list opening positions: %v", err)
	}
	now := time.Now()

	// localLegs: ticket → (leg, position)，收尾需要 position 引用
	type legWithPos struct {
		leg pgstore.PositionLeg
		pos pgstore.Position
	}
	localLegs := make(map[string]legWithPos)
	for _, p := range openPositions {
		sub, cancel := r.withTimeout(ctx)
		legs, err := r.store.ListPositionLegs(sub, p.ID)
		cancel()
		if err != nil {
			log.Printf("[reconcile] list legs pos %d: %v", p.ID, err)
			continue
		}
		for _, leg := range legs {
			if leg.OrderID != "" && leg.Phase == "OPEN" {
				localLegs[leg.OrderID] = legWithPos{leg: leg, pos: p}
			}
		}
	}

	// 2. 查 venue 实际持仓（每个 querier 独立超时，单个失败不阻断其余）
	venueOrders := make(map[string]VenueOrder) // ticket → order
	for _, q := range r.snapshot() {
		sub, cancel := r.withTimeout(ctx)
		orders, err := q.QueryOpenedOrders(sub)
		cancel()
		if err != nil {
			log.Printf("[reconcile] query venue %s: %v（本轮跳过）", q.VenueKey(), err)
			continue
		}
		for _, o := range orders {
			venueOrders[o.Ticket] = o
		}
	}

	// 3. 比对
	mismatches := 0

	// 本地有、venue 无 → 查证 OrderHistory → 命中收尾 / 未命中 critical（D-073+）
	for ticket, lp := range localLegs {
		if _, ok := venueOrders[ticket]; ok {
			continue // venue 有，正常
		}
		// 查证：调该 venue OrderHistory(ticket)
		hq := r.findHistoryQuerier(lp.leg.Venue)
		if hq == nil {
			// 缺陷2修复：查证器未就绪（CEX 无查证器 / adapter 未连）→ 不告警不计数，下轮复查
			log.Printf("[reconcile] 查证器未就绪 ticket=%s venue=%s，下轮复查", ticket, lp.leg.Venue)
			continue
		}
		sub, cancel := r.withTimeout(ctx)
		hist, err := hq.QueryOrderHistory(sub, ticket, 7) // 查近 7 天
		cancel()
		if err != nil {
			// 查证失败 = fail-closed（不动仓位 + critical 带错误）
			mismatches++
			msg := fmt.Sprintf("对账查证失败 position #%d ticket=%s venue=%s: %v（人工处置）", lp.pos.ID, ticket, lp.leg.Venue, err)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
			continue
		}
		if hist == nil {
			// 未命中（历史也无）→ 维持 critical（真失联）
			mismatches++
			msg := fmt.Sprintf("对账不一致：本地 leg ticket=%s venue=%s symbol=%s 在 venue 侧未找到（历史单也无，真失联）",
				ticket, lp.leg.Venue, lp.leg.Symbol)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
			continue
		}
		// 命中（已平仓）→ 自动收尾流水
		if err := r.autoClose(ctx, &lp.pos, lp.leg, hist); err != nil {
			// 收尾失败 = fail-closed（critical 带原因，下轮重试）
			mismatches++
			msg := fmt.Sprintf("对账自动收尾失败 position #%d ticket=%s: %v（下轮重试）", lp.pos.ID, ticket, err)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
			continue
		}
		// 收尾成功（warn 已在 autoClose 内插，不计 mismatches）
		log.Printf("[reconcile] auto-close completed pos=%d ticket=%s", lp.pos.ID, ticket)
	}

	// 3b. OPENING 僵尸收编（超 24h，D-073+）
	for _, p := range openingPositions {
		if !isOPENINGZombie(&p, now) {
			continue
		}
		if err := r.autoFailOPENINGZombie(ctx, &p, venueOrders); err != nil {
			mismatches++
			msg := fmt.Sprintf("OPENING 僵尸归档失败 position #%d: %v", p.ID, err)
			r.insertAlertCtx(ctx, "critical", msg)
			log.Printf("[reconcile] %s", msg)
		}
	}

	// venue 有、本地无 → 可能未记录的持仓（D-032：仅 ARB 源订单视为失联；系统外单不追踪）
	for ticket, vo := range venueOrders {
		if _, ok := localLegs[ticket]; ok {
			continue // 本地有，正常
		}
		if !isARBOrder(vo.Comment) {
			log.Printf("[reconcile] venue %s 持仓 ticket=%s symbol=%s comment=%q 为系统外单，不追踪",
				vo.Venue, ticket, vo.Symbol, vo.Comment)
			continue
		}
		mismatches++
		msg := fmt.Sprintf("对账不一致：venue %s 持仓 ticket=%s symbol=%s side=%s vol=%f 本地未记录（ARB 源订单失联）",
			vo.Venue, ticket, vo.Symbol, vo.Side, vo.Volume)
		r.insertAlertCtx(ctx, "critical", msg)
		log.Printf("[reconcile] %s", msg)
	}

	if mismatches == 0 {
		log.Printf("[reconcile] OK: %d local legs, %d venue orders, all matched", len(localLegs), len(venueOrders))
	}

	return mismatches, nil
}

// StartLoop 定期对账。阻塞至 ctx 取消。
// 缺陷1修复：initial Run 放独立 goroutine（受 runTimeout 保护，不阻塞 loop）。
// 缺陷2修复：initial 延迟 initialDelay（默认 60s，等 MT4/MT5 adapter 就绪，避免空查证器误报）。
func (r *Reconciler) StartLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 首轮延迟（等 adapter 就绪），独立 goroutine 跑（不阻塞 ticker loop）
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.initialDelay):
		}
		if _, err := r.Run(ctx); err != nil {
			log.Printf("[reconcile] initial: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.Run(ctx); err != nil {
				log.Printf("[reconcile] run: %v", err)
			}
		}
	}
}
