// gate_private_ws_events.go —— D-450 S2：Gate 私有 WS 事件帧分派与缓存/sink 落点
// （自 gate_private_ws.go 拆出，保 check-lines <300；行为零变更）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/venue"
)

// GatePrivateHooks D-570 包C S7：风险事件整帧转发钩（镜像 D-569 S7 MARGIN_CALL 裁定——
// 快照字段表明确但零实弹帧，不做字段级解析，payload=result 原文；nil 安全）。
type GatePrivateHooks struct {
	OnLiquidate     func(payload []byte) // futures.liquidates（自有强平单事件）
	OnPositionClose func(payload []byte) // futures.position_closes（平仓事件 pnl+side+text）
	OnADL           func(payload []byte) // futures.auto_deleverages+position_adl_rank 同槽（皆 ADL 域事件）
	OnRiskLimit     func(payload []byte) // futures.reduce_risk_limits（风控限额降档预警）
}

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
	// D-570 包C S7：风险频道族全整帧转发（审计 InsertCEXPrivateEvent 已在上方先行）。
	case "futures.liquidates":
		if g.hooks.OnLiquidate != nil {
			g.hooks.OnLiquidate(msg.Result)
		}
	case "futures.position_closes":
		if g.hooks.OnPositionClose != nil {
			g.hooks.OnPositionClose(msg.Result)
		}
	case "futures.auto_deleverages", "futures.position_adl_rank":
		if g.hooks.OnADL != nil {
			g.hooks.OnADL(msg.Result)
		}
	case "futures.reduce_risk_limits":
		if g.hooks.OnRiskLimit != nil {
			g.hooks.OnRiskLimit(msg.Result)
		}
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
		Currency  string `json:"currency"`
		Balance   string `json:"balance"`
		Available string `json:"available"`
	}
	if err := json.Unmarshal(result, &items); err != nil {
		return
	}
	for _, it := range items {
		if it.Currency != "USDT" {
			continue
		}
		eq, perr := f64z(it.Balance)
		if perr != nil {
			log.Printf("[gate-ws] drop field Balance: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		// D-542 S1：可用=available（缺字段回退 balance——不猜负值；脏字段整帧不采同 fail-closed）。
		avail := eq
		if it.Available != "" {
			a2, aerr := f64z(it.Available)
			if aerr != nil {
				log.Printf("[gate-ws] drop field Available: %v", aerr)
				return
			}
			avail = a2
		}
		g.balLedger.Put("gate:perp", avail, eq, "USDT", "ws", g.now())
		row := pgstore.VenueAccountSnapshot{
			Venue: "gate", VenueType: "perp",
			BalanceRaw: eq, EquityRaw: eq,
			Currency: "USDT", FetchedAt: g.now(), Src: "gate_event",
		}
		if err := g.sink.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
			log.Printf("[gate-ws] insert snapshot: %v", err)
		}
	}
	WarnUnconsumed("gate", "futures.balances", result, gateBalancesConsumed)
}

// onPositions futures.positions → 持仓全量快照替换缓存（推送帧本身即全量数组）。
func (g *GatePrivateWS) onPositions(result json.RawMessage) {
	// 字段名=REST 同 schema（cex_positions.go gate 结构体实名实证：unrealised_pnl/
	// pnl_fund/maintenance_margin；cex_private_events 实帧暂无行=组合未持仓未触发推送，
	// 部署后首帧 WarnUnconsumed 兜底核对）。
	var items []struct {
		Contract      string `json:"contract"`
		Size          int64  `json:"size"`
		EntryPrice    string `json:"entry_price"`
		Margin        string `json:"margin"`
		UnrealisedPnl string `json:"unrealised_pnl"`
		PnlFund       string `json:"pnl_fund"`           // D-477 S2：funding P&L=carry 语义最高优先
		MaintMargin   string `json:"maintenance_margin"` // D-477 S2
		// open_time/update_time 帧形态未知（实帧表暂无行）→ any 宽容接（数字/字符串双容，
		// 秒级 epoch；禁类型过严导致整帧解析失败）。
		OpenTime   any `json:"open_time"`
		UpdateTime any `json:"update_time"`
	}
	if err := json.Unmarshal(result, &items); err != nil {
		return
	}
	now := g.now()
	orders := make([]venue.Order, 0, len(items))
	for _, it := range items {
		ep, perr := f64z(it.EntryPrice)
		if perr != nil {
			log.Printf("[gate-ws] drop field EntryPrice: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		mg, perr := f64z(it.Margin)
		if perr != nil {
			log.Printf("[gate-ws] drop field Margin: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		upl, perr := f64z(it.UnrealisedPnl)
		if perr != nil {
			log.Printf("[gate-ws] drop field UnrealisedPnl: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		fund, perr := f64z(it.PnlFund)
		if perr != nil {
			log.Printf("[gate-ws] drop field PnlFund: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		mm, perr := f64z(it.MaintMargin)
		if perr != nil {
			log.Printf("[gate-ws] drop field MaintMargin: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		side, ps := "BUY", "long"
		vol := float64(it.Size)
		if it.Size < 0 {
			side, ps, vol = "SELL", "short", -vol
		}
		if q, err := g.gateQuanto(context.Background(), it.Contract); err == nil {
			vol *= q
		}
		// D-477 S2：open_time 优先 update_time 回退（秒级 epoch；帧无 → now 兜底）。
		opened := now
		if sec := anyToSec(it.OpenTime); sec > 0 {
			opened = time.Unix(int64(sec), 0)
		} else if sec := anyToSec(it.UpdateTime); sec > 0 {
			opened = time.Unix(int64(sec), 0)
		}
		orders = append(orders, venue.Order{
			Ticket: it.Contract, Symbol: it.Contract, Side: side, Volume: vol, OpenPrice: ep,
			Profit: upl, Swap: fund, MaintMargin: mm, // D-477 S2 GAP-B2 全量补消费
			Venue: "gate", VenueKey: g.venueKey, PosSide: ps,
			InitialMargin: mg, OpenedAt: opened,
		})
	}
	g.posCache.PutPositions(g.venueKey, orders, now)
	WarnUnconsumed("gate", "futures.positions", result, gatePositionsConsumed)
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
		Fee       float64     `json:"fee"` // D-570 包B S4③：帧原生 fee（快照 :4376 JSON number 实弹；USDT 本位 fee=USD 口径钉死）
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
		px, perr := f64z(it.FillPrice)
		if perr != nil {
			log.Printf("[gate-ws] drop field FillPrice: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		qty := float64(filledContracts)
		if q, err := g.gateQuanto(ctx, it.Contract); err == nil {
			qty *= q // 合约数→基础币（与 executor Fill.Qty 契约一致）
		}
		g.orderCache.PutOrder(g.venueKey, it.ID.String(), it.Text, OrderEvent{
			State: state, AccFillQty: qty, AvgPx: px, FeeUSD: it.Fee, At: g.now(),
		})
	}
	WarnUnconsumed("gate", "futures.orders", result, gateOrdersConsumed)
}

// D-475b S2 consumed 清单（契约对偶面）。
var (
	gatePositionsConsumed = []string{"contract", "size", "entry_price", "margin", "unrealised_pnl", "pnl_fund", "maintenance_margin", "open_time", "update_time"} // D-477 S2：GAP-B2 全量补消费
	gateOrdersConsumed    = []string{"id", "text", "contract", "status", "finish_as", "size", "left", "fill_price", "fee"}
	gateBalancesConsumed  = []string{"currency", "balance", "available"} // D-542：+available=可用余额
)

func abs64(v int64) int64 { return int64(math.Abs(float64(v))) }

// anyToSec D-477 S2：json 数字/字符串 → 秒级 epoch float（无效/缺省返 0）。
func anyToSec(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case json.Number:
		f, _ := x.Float64()
		return f
	case string:
		f, perr := f64z(x)
		if perr != nil {
			log.Printf("[gate-ws] anyToSec dirty %q: %v", x, perr)
			return 0
		}
		return f
	}
	return 0
}
