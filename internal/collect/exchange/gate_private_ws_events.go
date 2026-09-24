// gate_private_ws_events.go —— D-450 S2：Gate 私有 WS 事件帧分派与缓存/sink 落点
// （自 gate_private_ws.go 拆出，保 check-lines <300；行为零变更）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"strconv"

	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// handleFrame 帧分派：subscribe ACK 打日志；update 事件先落审计表（失败 warn 仍更缓存，R3）
// 再按频道分发；error fail-loud 返回错误触发重连。
func (g *GatePrivateWS) handleFrame(ctx context.Context, data []byte) error {
	var msg struct {
		Channel string          `json:"channel"`
		Event   string          `json:"event"`
		Error   json.RawMessage `json:"error"`
		Result  json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &msg); err != nil {
		return nil // 非 JSON 帧跳过
	}
	if len(msg.Error) > 0 && string(msg.Error) != "null" {
		return fmt.Errorf("gate private event error: %s", truncateStr(string(msg.Error), 200))
	}
	switch msg.Event {
	case "subscribe":
		log.Printf("[gate-ws] subscribed %s", msg.Channel)
		return nil
	case "update":
	default:
		return nil // ping/pong 控制帧与其他事件
	}
	// 审计先行（payload=result 原文）。
	if g.events != nil {
		key := gateEventKey(msg.Channel, msg.Result)
		if err := g.events.InsertCEXPrivateEvent(ctx, "gate", "perp", msg.Channel, key, msg.Result); err != nil {
			log.Printf("[gate-ws] audit insert %s: %v (cache update proceeds)", msg.Channel, err)
		}
	}
	switch msg.Channel {
	case "futures.balances":
		g.onBalances(ctx, msg.Result)
	case "futures.positions":
		g.onPositions(msg.Result)
	case "futures.orders":
		g.onOrders(ctx, msg.Result)
	}
	return nil
}

// gateEventKey 审计行关联键：orders 取 id、usertrades 取 id、其余取频道名。
func gateEventKey(channel string, result json.RawMessage) string {
	var items []struct {
		ID json.Number `json:"id"`
	}
	if json.Unmarshal(result, &items) == nil && len(items) > 0 && items[0].ID.String() != "" {
		return items[0].ID.String()
	}
	return channel
}

// onBalances futures.balances → USDT 行余额落快照（src=gate_event）。
// demo 流禁写快照——REST 写面 IsRealAccount 只收 real（返修 R1）。
// 字段名按 Gate 文档 {currency,balance,change,...}——生产首帧由日志/审计表原文复核。
func (g *GatePrivateWS) onBalances(ctx context.Context, result json.RawMessage) {
	if g.simulated {
		return
	}
	var items []struct {
		Currency string `json:"currency"`
		Balance  string `json:"balance"`
	}
	if err := json.Unmarshal(result, &items); err != nil {
		return
	}
	for _, it := range items {
		if it.Currency != "USDT" {
			continue
		}
		eq, _ := strconv.ParseFloat(it.Balance, 64)
		row := pgstore.VenueAccountSnapshot{
			Venue: "gate", VenueType: "perp",
			BalanceRaw: eq, EquityRaw: eq,
			Currency: "USDT", FetchedAt: g.now(), Src: "gate_event",
		}
		if err := g.sink.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
			log.Printf("[gate-ws] insert snapshot: %v", err)
		}
	}
}

// onPositions futures.positions → 持仓全量快照替换缓存（推送帧本身即全量数组）。
func (g *GatePrivateWS) onPositions(result json.RawMessage) {
	var items []struct {
		Contract   string `json:"contract"`
		Size       int64  `json:"size"`
		EntryPrice string `json:"entry_price"`
		Margin     string `json:"margin"`
	}
	if err := json.Unmarshal(result, &items); err != nil {
		return
	}
	now := g.now()
	orders := make([]reconcile.VenueOrder, 0, len(items))
	for _, it := range items {
		ep, _ := strconv.ParseFloat(it.EntryPrice, 64)
		mg, _ := strconv.ParseFloat(it.Margin, 64)
		side, ps := "BUY", "long"
		vol := float64(it.Size)
		if it.Size < 0 {
			side, ps, vol = "SELL", "short", -vol
		}
		if q, err := g.gateQuanto(context.Background(), it.Contract); err == nil {
			vol *= q
		}
		orders = append(orders, reconcile.VenueOrder{
			Symbol: it.Contract, Side: side, Volume: vol, OpenPrice: ep,
			Venue: "gate", VenueKey: g.venueKey, PosSide: ps,
			InitialMargin: mg, OpenedAt: now,
		})
	}
	g.posCache.PutPositions(g.venueKey, orders, now)
}

// onOrders futures.orders → 订单事件缓存（state 归一与 exec.OrderStatusQuerier 同域）。
func (g *GatePrivateWS) onOrders(ctx context.Context, result json.RawMessage) {
	var items []struct {
		ID        json.Number `json:"id"`
		Text      string      `json:"text"` // 客户端自定义 id
		Contract  string      `json:"contract"`
		Status    string      `json:"status"`    // open / finished
		FinishAs  string      `json:"finish_as"` // filled / cancelled / ...
		Size      int64       `json:"size"`
		Left      int64       `json:"left"`
		FillPrice string      `json:"fill_price"`
	}
	if err := json.Unmarshal(result, &items); err != nil {
		return
	}
	for _, it := range items {
		state := "live"
		switch it.Status {
		case "finished":
			if it.FinishAs == "filled" {
				state = "filled"
			} else {
				state = "canceled"
			}
		case "open":
			if abs64(it.Left) < abs64(it.Size) {
				state = "partial"
			}
		}
		filledContracts := abs64(it.Size - it.Left)
		px, _ := strconv.ParseFloat(it.FillPrice, 64)
		qty := float64(filledContracts)
		if q, err := g.gateQuanto(ctx, it.Contract); err == nil {
			qty *= q // 合约数→基础币（与 executor Fill.Qty 契约一致）
		}
		g.orderCache.PutOrder(g.venueKey, it.ID.String(), it.Text, OrderEvent{
			State: state, AccFillQty: qty, AvgPx: px, At: g.now(),
		})
	}
}

func abs64(v int64) int64 { return int64(math.Abs(float64(v))) }
