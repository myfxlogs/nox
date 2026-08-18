// Package reconcile 实现持仓对账（§13.4 B2）。
// 查 venue 实际持仓与本地 OPEN legs 比对，不一致 → critical 告警。
package reconcile

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"arb/internal/adapter/gen/mtapi/mt4"
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
	"arb/internal/store/pgstore"
)

// VenueQuerier 查询 venue 实际持仓的统一接口。
type VenueQuerier interface {
	QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error)
}

// VenueOrder venue 侧的持仓记录。
type VenueOrder struct {
	Ticket   string
	Symbol   string
	Side     string // BUY / SELL
	Volume   float64
	OpenPrice float64
	Venue    string
}

// MT4Querier 通过 MT4 gRPC 查询持仓。
type MT4Querier struct {
	token   string
	mt4     mt4.MT4Client
	venue   string
}

func NewMT4Querier(venue, token string, mt4Client mt4.MT4Client) *MT4Querier {
	return &MT4Querier{venue: venue, token: token, mt4: mt4Client}
}

func (q *MT4Querier) QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error) {
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
			Ticket:    strconv.FormatInt(int64(ord.GetTicket()), 10),
			Symbol:    ord.GetSymbol(),
			Side:      side,
			Volume:    ord.GetLots(),
			OpenPrice: ord.GetOpenPrice(),
			Venue:     q.venue,
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

func (q *MT5Querier) QueryOpenedOrders(ctx context.Context) ([]VenueOrder, error) {
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
			Ticket:    strconv.FormatInt(ord.GetTicket(), 10),
			Symbol:    ord.GetSymbol(),
			Side:      side,
			Volume:    ord.GetLots(),
			OpenPrice: ord.GetOpenPrice(),
			Venue:     q.venue,
		})
	}
	return out, nil
}

// Reconciler 对账器：比对本地 OPEN legs 与 venue 实际持仓。
type Reconciler struct {
	store    *pgstore.Store
	queriers []VenueQuerier
}

// New 创建对账器。
func New(store *pgstore.Store, queriers ...VenueQuerier) *Reconciler {
	return &Reconciler{store: store, queriers: queriers}
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
	for _, q := range r.queriers {
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

	// venue 有、本地无 → 可能未记录的持仓
	for ticket, vo := range venueOrders {
		if _, ok := localLegs[ticket]; !ok {
			mismatches++
			msg := fmt.Sprintf("对账不一致：venue %s 持仓 ticket=%s symbol=%s side=%s vol=%f 本地未记录",
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
