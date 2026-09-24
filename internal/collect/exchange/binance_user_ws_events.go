// binance_user_ws_events.go —— D-450 S5：Binance 用户流事件帧分派与缓存/sink 落点
// （自 binance_user_ws.go 拆出，保 check-lines <300；行为零变更）。
package exchange

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"

	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
)

// handleFrame 用户流事件分派：先落审计表（失败 warn 仍更缓存，R3）再更新缓存/sink。
func (b *BinanceUserWS) handleFrame(ctx context.Context, data []byte) {
	var env struct {
		E string          `json:"e"`
		O json.RawMessage `json:"o"`
		A json.RawMessage `json:"a"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return
	}
	switch env.E {
	case "ORDER_TRADE_UPDATE":
		b.onAudit(ctx, "orders", env.O)
		b.onOrderUpdate(env.O)
	case "ACCOUNT_UPDATE":
		b.onAudit(ctx, "account", env.A)
		b.onAccountUpdate(ctx, env.A)
	case "listenKeyExpired":
		// 官方显式失效事件——靠 read 断开自然触发重取；此处仅记录。
		log.Printf("[binance-user-ws] listenKeyExpired event")
	default:
		return
	}
}

func (b *BinanceUserWS) onAudit(ctx context.Context, channel string, payload json.RawMessage) {
	if b.events == nil || len(payload) == 0 {
		return
	}
	if err := b.events.InsertCEXPrivateEvent(ctx, "binance", b.venueType, channel, "", payload); err != nil {
		log.Printf("[binance-user-ws] audit insert %s: %v (cache update proceeds)", channel, err)
	}
}

// onOrderUpdate ORDER_TRADE_UPDATE → 订单事件缓存。
// o 字段：i=orderID、c=clOrdID、X=状态、z=累计成交量、ap=均价、n=手续费。
func (b *BinanceUserWS) onOrderUpdate(o json.RawMessage) {
	var ord struct {
		I  json.Number `json:"i"`
		C  string      `json:"c"`
		X  string      `json:"X"`
		Z  string      `json:"z"`
		AP string      `json:"ap"`
		L  string      `json:"L"`
		N  string      `json:"n"`
	}
	if err := json.Unmarshal(o, &ord); err != nil {
		return
	}
	state := "live"
	switch ord.X {
	case "FILLED":
		state = "filled"
	case "PARTIALLY_FILLED":
		state = "partial"
	case "CANCELED", "REJECTED", "EXPIRED":
		state = "canceled"
	}
	qty, _ := strconv.ParseFloat(ord.Z, 64)
	px, _ := strconv.ParseFloat(ord.AP, 64)
	if px == 0 {
		px, _ = strconv.ParseFloat(ord.L, 64) // ap 缺省回落最后成交价
	}
	fee, _ := strconv.ParseFloat(ord.N, 64)
	b.orderCache.PutOrder(b.venueKey, ord.I.String(), ord.C, OrderEvent{
		State: state, AccFillQty: qty, AvgPx: px, FeeUSD: fee, At: b.now(),
	})
}

// onAccountUpdate ACCOUNT_UPDATE → a.B USDT wb 落快照（src=binance_event）+ a.P 持仓全量替换缓存。
func (b *BinanceUserWS) onAccountUpdate(ctx context.Context, a json.RawMessage) {
	var acct struct {
		B []struct {
			A  string `json:"a"`
			WB string `json:"wb"`
		} `json:"B"`
		P []struct {
			S  string `json:"s"`
			PA string `json:"pa"`
			EP string `json:"ep"`
			IW string `json:"iw"`
			PS string `json:"ps"`
		} `json:"P"`
	}
	if err := json.Unmarshal(a, &acct); err != nil {
		return
	}
	now := b.now()
	for _, bal := range acct.B {
		if bal.A != "USDT" {
			continue
		}
		eq, _ := strconv.ParseFloat(bal.WB, 64)
		row := pgstore.VenueAccountSnapshot{
			Venue: "binance", VenueType: b.venueType,
			BalanceRaw: eq, EquityRaw: eq,
			Currency: "USDT", FetchedAt: now, Src: "binance_event",
		}
		if err := b.sink.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
			log.Printf("[binance-user-ws] insert snapshot: %v", err)
		}
	}
	if len(acct.P) == 0 {
		return
	}
	orders := make([]reconcile.VenueOrder, 0, len(acct.P))
	for _, p := range acct.P {
		pa, _ := strconv.ParseFloat(p.PA, 64)
		ep, _ := strconv.ParseFloat(p.EP, 64)
		iw, _ := strconv.ParseFloat(p.IW, 64)
		side := "BUY"
		if pa < 0 {
			side, pa = "SELL", -pa
		}
		orders = append(orders, reconcile.VenueOrder{
			Symbol: p.S, Side: side, Volume: pa, OpenPrice: ep,
			Venue: "binance", VenueKey: b.venueKey, PosSide: strings.ToLower(p.PS),
			InitialMargin: iw, OpenedAt: now,
		})
	}
	b.posCache.PutPositions(b.venueKey, orders, now)
}
