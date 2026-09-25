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
// 返回 true=帧要求关连接（listenKeyExpired）——调用方须结束 runOnce 让 Run 重取 key。
func (b *BinanceUserWS) handleFrame(ctx context.Context, data []byte) (expired bool) {
	var env struct {
		E string          `json:"e"`
		O json.RawMessage `json:"o"`
		A json.RawMessage `json:"a"`
	}
	if err := json.Unmarshal(data, &env); err != nil {
		return false
	}
	switch env.E {
	case "ORDER_TRADE_UPDATE":
		b.onAudit(ctx, "orders", env.O)
		b.onOrderUpdate(env.O)
	case "ACCOUNT_UPDATE":
		b.onAudit(ctx, "account", env.A)
		b.onAccountUpdate(ctx, env.A)
	case "executionReport": // spot 订单更新（载荷=扁平字段，非 perp 的 o 包裹）
		b.onAudit(ctx, "orders", data)
		b.onSpotExecutionReport(data)
	case "outboundAccountPosition": // spot 账户持仓全量快照
		b.onAudit(ctx, "account", data)
		b.onSpotAccountPosition(ctx, data)
	case "balanceUpdate": // spot 余额增量——审计留痕即可（outboundAccountPosition 同帧带全量）
		b.onAudit(ctx, "account", data)
	case "listenKeyExpired":
		// 官方显式失效事件——ping 保活不断流会掩盖死 key；关连接触发 runOnce 返回重取。
		log.Printf("[binance-user-ws] listenKeyExpired event (reconnecting for fresh key)")
		return true
	}
	return false
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
	WarnUnconsumed("binance", "ORDER_TRADE_UPDATE", o, binanceOrderUpdateConsumed)
}

// onAccountUpdate ACCOUNT_UPDATE → a.B USDT wb 落快照（src=binance_event）+ a.P 持仓全量替换缓存。
func (b *BinanceUserWS) onAccountUpdate(ctx context.Context, a json.RawMessage) {
	var acct struct {
		M string `json:"m"` // D-469a S3：事件原因（FUNDING_FEE/DEPOSIT/WITHDRAW/ORDER…）
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
			UP string `json:"up"` // D-477 S1：未实现盈亏（GAP-B1 补消费——OKX upl→Profit 同语义）
		} `json:"P"`
	}
	if err := json.Unmarshal(a, &acct); err != nil {
		return
	}
	now := b.now()
	// demo 流禁写快照——REST 写面 IsRealAccount 只收 real（返修 R1），持仓/订单缓存仍更新。
	if !b.simulated {
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
	}
	// D-469a S3：事件原因分派——funding_fee kick income，出入账 kick io（a.m 缺省不 kick）。
	if b.hooks.OnFundingFee != nil && acct.M == "FUNDING_FEE" {
		b.hooks.OnFundingFee()
	}
	if b.hooks.OnIO != nil && (acct.M == "DEPOSIT" || acct.M == "WITHDRAW") {
		b.hooks.OnIO()
	}
	if len(acct.P) == 0 {
		return
	}
	orders := make([]reconcile.VenueOrder, 0, len(acct.P))
	for _, p := range acct.P {
		pa, _ := strconv.ParseFloat(p.PA, 64)
		ep, _ := strconv.ParseFloat(p.EP, 64)
		iw, _ := strconv.ParseFloat(p.IW, 64)
		up, _ := strconv.ParseFloat(p.UP, 64)
		side := "BUY"
		if pa < 0 {
			side, pa = "SELL", -pa
		}
		orders = append(orders, reconcile.VenueOrder{
			Ticket: p.S, Symbol: p.S, Side: side, Volume: pa, OpenPrice: ep,
			Profit: up, // D-477 S1 GAP-B1：up→Profit（REST 基线 cex_positions.go 以 symbol 作 Ticket）
			Venue:  "binance", VenueKey: b.venueKey, PosSide: strings.ToLower(p.PS),
			InitialMargin: iw, OpenedAt: now,
		})
	}
	b.posCache.PutPositions(b.venueKey, orders, now)
	WarnUnconsumed("binance", "ACCOUNT_UPDATE", a, binanceAccountUpdateConsumed)
}

// onSpotExecutionReport spot executionReport → 订单事件缓存。
// 字段：i=orderID、c=clOrdID、X=状态、z=累计成交量、L=最后成交价、n=手续费。
func (b *BinanceUserWS) onSpotExecutionReport(data []byte) {
	var ord struct {
		I int64  `json:"i"`
		C string `json:"c"`
		X string `json:"X"`
		Z string `json:"z"`
		L string `json:"L"`
		N string `json:"n"`
	}
	if err := json.Unmarshal(data, &ord); err != nil || ord.I == 0 {
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
	px, _ := strconv.ParseFloat(ord.L, 64)
	fee, _ := strconv.ParseFloat(ord.N, 64)
	b.orderCache.PutOrder(b.venueKey, strconv.FormatInt(ord.I, 10), ord.C, OrderEvent{
		State: state, AccFillQty: qty, AvgPx: px, FeeUSD: fee, At: b.now(),
	})
	WarnUnconsumed("binance", "executionReport", data, binanceExecReportConsumed)
}

// D-475b S2 consumed 清单（a.B/a.P 嵌套键同名展开——walkJSON 递归扫嵌套层）。
var (
	binanceOrderUpdateConsumed   = []string{"i", "c", "X", "z", "ap", "L", "n"}
	binanceAccountUpdateConsumed = []string{"m", "B", "a", "wb", "P", "s", "pa", "ep", "iw", "ps", "up"} // D-477 S1：+up→Profit
	binanceExecReportConsumed    = []string{"i", "c", "X", "z", "L", "n"}
	binanceSpotAcctPosConsumed   = []string{"B", "a", "f", "l"}
)

// onSpotAccountPosition outboundAccountPosition → B[{a,f,l}] USDT free+locked 落快照。
// spot 无持仓概念——持仓缓存不动（订单侧由 executionReport 覆盖）。
func (b *BinanceUserWS) onSpotAccountPosition(ctx context.Context, data []byte) {
	var acct struct {
		B []struct {
			A string `json:"a"`
			F string `json:"f"`
			L string `json:"l"`
		} `json:"B"`
	}
	if err := json.Unmarshal(data, &acct); err != nil {
		return
	}
	if b.simulated {
		return
	}
	for _, bal := range acct.B {
		if bal.A != "USDT" {
			continue
		}
		free, _ := strconv.ParseFloat(bal.F, 64)
		locked, _ := strconv.ParseFloat(bal.L, 64)
		eq := free + locked
		row := pgstore.VenueAccountSnapshot{
			Venue: "binance", VenueType: b.venueType,
			BalanceRaw: eq, EquityRaw: eq,
			Currency: "USDT", FetchedAt: b.now(), Src: "binance_event",
		}
		if err := b.sink.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
			log.Printf("[binance-user-ws] insert spot snapshot: %v", err)
		}
	}
	WarnUnconsumed("binance", "outboundAccountPosition", data, binanceSpotAcctPosConsumed)
}
