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
	store    *pgstore.Store
	mu       sync.RWMutex
	queriers []VenueQuerier
}

// New 创建对账器。
func New(store *pgstore.Store, queriers ...VenueQuerier) *Reconciler {
	return &Reconciler{store: store, queriers: queriers}
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

// Run 执行一次对账。返回不一致数量。
func (r *Reconciler) Run(ctx context.Context) (int, error) {
	// 1. 查本地所有 OPEN legs
	positions, err := r.store.ListPositions(ctx, "OPEN", 200, 0)
	if err != nil {
		return 0, fmt.Errorf("list open positions: %w", err)
	}
	localLegs := make(map[string]pgstore.PositionLeg) // orderID → leg
	for _, p := range positions {
		legs, err := r.store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			log.Printf("[reconcile] list legs pos %d: %v", p.ID, err)
			continue
		}
		for _, leg := range legs {
			if leg.OrderID != "" {
				localLegs[leg.OrderID] = leg
			}
		}
	}

	// 2. 查 venue 实际持仓
	venueOrders := make(map[string]VenueOrder) // ticket → order
	for _, q := range r.snapshot() {
		orders, err := q.QueryOpenedOrders(ctx)
		if err != nil {
			log.Printf("[reconcile] query venue: %v", err)
			continue
		}
		for _, o := range orders {
			venueOrders[o.Ticket] = o
		}
	}

	// 3. 比对
	mismatches := 0

	// 本地有、venue 无 → 可能已平仓但本地未同步
	for ticket, leg := range localLegs {
		if _, ok := venueOrders[ticket]; !ok {
			mismatches++
			msg := fmt.Sprintf("对账不一致：本地 leg ticket=%s venue=%s symbol=%s 在 venue 侧未找到（可能已平仓）",
				ticket, leg.Venue, leg.Symbol)
			if err := r.store.InsertAlert(ctx, nil, "critical", msg); err != nil {
				log.Printf("[reconcile] alert: %v", err)
			}
			log.Printf("[reconcile] %s", msg)
		}
	}

	// venue 有、本地无 → 可能未记录的持仓（D-032：仅 ARB 源订单视为失联；系统外单不追踪）
	for ticket, vo := range venueOrders {
		if _, ok := localLegs[ticket]; !ok {
			if !isARBOrder(vo.Comment) {
				log.Printf("[reconcile] venue %s 持仓 ticket=%s symbol=%s comment=%q 为系统外单，不追踪",
					vo.Venue, ticket, vo.Symbol, vo.Comment)
				continue
			}
			mismatches++
			msg := fmt.Sprintf("对账不一致：venue %s 持仓 ticket=%s symbol=%s side=%s vol=%f 本地未记录（ARB 源订单失联）",
				vo.Venue, ticket, vo.Symbol, vo.Side, vo.Volume)
			if err := r.store.InsertAlert(ctx, nil, "critical", msg); err != nil {
				log.Printf("[reconcile] alert: %v", err)
			}
			log.Printf("[reconcile] %s", msg)
		}
	}

	if mismatches == 0 {
		log.Printf("[reconcile] OK: %d local legs, %d venue orders, all matched", len(localLegs), len(venueOrders))
	}

	return mismatches, nil
}

// StartLoop 定期对账。阻塞至 ctx 取消。
func (r *Reconciler) StartLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 启动时立即对账一次
	if _, err := r.Run(ctx); err != nil {
		log.Printf("[reconcile] initial: %v", err)
	}

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
