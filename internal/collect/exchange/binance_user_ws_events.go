// binance_user_ws_events.go —— D-450 S5：Binance 用户流事件帧分派与缓存/sink 落点
// （自 binance_user_ws.go 拆出，保 check-lines <300；行为零变更）。
package exchange

import (
	"context"
	"encoding/json"
	"log"
	"math"
	"strings"

	"arb/internal/store/pgstore"
	"arb/internal/venue"
)

// handleFrame 用户流事件分派：先落审计表（失败 warn 仍更缓存，R3）再更新缓存/sink。
// 返回 true=帧要求关连接（listenKeyExpired）——调用方须结束 runOnce 让 Run 重取 key。
func (b *BinanceUserWS) handleFrame(ctx context.Context, data []byte) (expired bool) {
	// D-569 包B S7：map 精确键查表——原 struct 解包在真帧 "E"(eventTime 数字) 撞
	// env.E(tag "e" string) 大小写不敏感匹配报类型错早退（全帧静默丢=隐形 bug）。
	var env map[string]json.RawMessage
	if err := json.Unmarshal(data, &env); err != nil {
		return false
	}
	var envE string
	if eRaw, ok := env["e"]; !ok || json.Unmarshal(eRaw, &envE) != nil {
		return false
	}
	envO, envA := env["o"], env["a"]
	switch envE {
	case "ORDER_TRADE_UPDATE":
		b.onAudit(ctx, "orders", envO)
		b.onOrderUpdate(envO)
	case "ACCOUNT_UPDATE":
		b.onAudit(ctx, "account", envA)
		b.onAccountUpdate(ctx, envA)
	case "executionReport": // spot 订单更新（载荷=扁平字段，非 perp 的 o 包裹）
		b.onAudit(ctx, "orders", data)
		b.onSpotExecutionReport(data)
	case "outboundAccountPosition": // spot 账户持仓全量快照
		b.onAudit(ctx, "account", data)
		b.onSpotAccountPosition(ctx, data)
	case "balanceUpdate": // spot 余额增量——审计留痕即可（outboundAccountPosition 同帧带全量）
		b.onAudit(ctx, "account", data)
	case "MARGIN_CALL":
		// D-569 包B S7：MARGIN_CALL schema 官方四路不可得——payload=全帧 data 整帧转发（api-response-fields-empirical）。
		b.onAudit(ctx, "margin_call", data)
		if b.hooks.OnMarginCall != nil {
			b.hooks.OnMarginCall(data)
		}
	case "listenKeyExpired":
		// 官方显式失效事件——ping 保活不断流会掩盖死 key；关连接触发 runOnce 返回重取。
		log.Printf("[binance-user-ws] listenKeyExpired event (reconnecting for fresh key)")
		return true
	default:
		// D-569 包B S7：未知事件类型留痕一行（新事件静默吞=黑盒）。
		log.Printf("[binance-user-ws] unhandled event e=%q venueType=%s", envE, b.venueType)
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

// jstr map 精确键 string 提取（D-584：map 键大小写敏感——"n"金额/"N"币种物理分离；
// 缺失/类型不符返 ""，消费面沿空值路径处理）。
func jstr(m map[string]json.RawMessage, k string) string {
	raw, ok := m[k]
	if !ok {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		return ""
	}
	return s
}

// jnum map 精确键 json.Number 提取（缺失返零值 Number，Int64()/String() 沿空值路径）。
func jnum(m map[string]json.RawMessage, k string) (n json.Number) {
	raw, ok := m[k]
	if !ok {
		return
	}
	if json.Unmarshal(raw, &n) != nil {
		return ""
	}
	return n
}

// onOrderUpdate ORDER_TRADE_UPDATE → 订单事件缓存。
// D-584：订单体改 map 精确键提取——struct tag 大小写不敏感使 "n"(金额) 撞 "N"(币种)
// 后写覆盖 → ParseFloat 崩整帧丢（生产实证 drop field N）；同帧 i/I、x/X、ap/AP(激活价)
// 同族。官方 schema 键名即语义：i=订单号 c=客户端单号 X=状态 z=累计成交量 ap=均价
// L=末次成交价 n=佣金金额；N=佣金币种（非数字）不消费不解析。
func (b *BinanceUserWS) onOrderUpdate(o json.RawMessage) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(o, &m); err != nil {
		return
	}
	state := "live"
	switch jstr(m, "X") {
	case "FILLED":
		state = "filled"
	case "PARTIALLY_FILLED":
		state = "partial"
	case "CANCELED", "REJECTED", "EXPIRED":
		state = "canceled"
	}
	qty, perr := f64z(jstr(m, "z"))
	if perr != nil {
		log.Printf("[binance-ws] drop field z: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
		return
	}
	px, perr := f64z(jstr(m, "ap"))
	if perr != nil {
		log.Printf("[binance-ws] drop field ap: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
		return
	}
	if px == 0 {
		pv, perr := f64z(jstr(m, "L"))
		if perr != nil {
			log.Printf("[binance-ws] drop field L: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		px = pv // ap 缺省回落最后成交价
	}
	fee, perr := f64z(jstr(m, "n"))
	if perr != nil {
		log.Printf("[binance-ws] drop field n: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
		return
	}
	// D-599 S1：n 是 N 币种计价的累计佣金——非 USD 系币种（BNB 返佣等）不冒称 USD，
	// 置 0 由 REST userTrades 兜底通道接管（honest-zero，不写假值）。
	feeUSD := 0.0
	if ccy := jstr(m, "N"); ccy == "USDT" || ccy == "USD" || ccy == "USDC" {
		feeUSD = fee
	}
	b.orderCache.PutOrder(b.venueKey, jnum(m, "i").String(), jstr(m, "c"), OrderEvent{
		State: state, AccFillQty: qty, AvgPx: px, FeeUSD: feeUSD, At: b.now(),
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
	// D-142：先解 ΣUP/Σiw——B 行快照 equity=wb+ΣUP（与 REST totalMarginBalance 同口径）；
	// 本帧无 P 行 → posCache 现存持仓 UP/IW 兜底（B/P 独立到达，勿以 0 覆盖真值）。
	var upSum, iwSum float64
	var orders []venue.Order
	pFresh := len(acct.P) > 0
	if pFresh {
		orders = make([]venue.Order, 0, len(acct.P))
		for _, p := range acct.P {
			pa, perr := f64z(p.PA)
			if perr != nil {
				log.Printf("[binance-ws] drop field PA: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
				return
			}
			if pa == 0 {
				// D-584 S4（b51a8c61 增收）：venue 平仓后推零量行——pa=0=该 symbol 无仓，
				// 全量替换语义下不建仓即正确（照建=dashboard 幻影仓）；iw/up 不累加该条。
				// D-015 ②（R1）：pa=0 行=该腿已平 → recon.Kick 叫醒对账（posSide 原样透传，不落库）。
				if b.hooks.OnPositionClosed != nil {
					b.hooks.OnPositionClosed(b.venueKey, p.S, p.PS)
				}
				continue
			}
			ep, perr := f64z(p.EP)
			if perr != nil {
				log.Printf("[binance-ws] drop field EP: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
				return
			}
			iw, perr := f64z(p.IW)
			if perr != nil {
				log.Printf("[binance-ws] drop field IW: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
				return
			}
			up, perr := f64z(p.UP)
			if perr != nil {
				log.Printf("[binance-ws] drop field UP: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
				return
			}
			side := "BUY"
			if pa < 0 {
				side, pa = "SELL", -pa
			}
			upSum += up
			iwSum += iw
			orders = append(orders, venue.Order{
				Ticket: p.S, Symbol: p.S, Side: side, Volume: pa, OpenPrice: ep,
				Profit: up, // D-477 S1 GAP-B1：up→Profit（REST 基线 cex_positions.go 以 symbol 作 Ticket）
				Venue:  "binance", VenueKey: b.venueKey, PosSide: strings.ToLower(p.PS),
				InitialMargin: iw, OpenedAt: now,
			})
		}
	} else if poss, ok := b.posCache.get(b.venueKey, EventFreshTTL, now); ok {
		for _, p := range poss {
			upSum += p.Profit
			iwSum += p.InitialMargin
		}
	}
	wbUSDT := math.NaN()
	// demo 流禁写快照——REST 写面 IsRealAccount 只收 real（返修 R1），持仓/订单缓存仍更新。
	if !b.simulated {
		for _, bal := range acct.B {
			if bal.A != "USDT" {
				continue
			}
			wb, perr := f64z(bal.WB)
			if perr != nil {
				log.Printf("[binance-ws] drop field WB: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
				return
			}
			wbUSDT = wb
			eq := wb + upSum // D-142：快照 equity=wb+ΣUP
			row := pgstore.VenueAccountSnapshot{
				Venue: "binance", VenueType: b.venueType,
				BalanceRaw: wb, EquityRaw: eq,
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
	if pFresh {
		b.posCache.PutPositions(b.venueKey, orders, now)
	}
	b.balPut(wbUSDT, upSum, iwSum, now)
	WarnUnconsumed("binance", "ACCOUNT_UPDATE", a, binanceAccountUpdateConsumed)
}

// balPut/onSpotAccountPosition D-542 活账本写口与 spot 余额帧处理见 binance_user_ws_balance.go。
// onSpotExecutionReport spot executionReport 帧处理见 binance_user_ws_spot_events.go（D-599 行数拆分）。

// D-475b S2 consumed 清单（a.B/a.P 嵌套键同名展开——walkJSON 递归扫嵌套层）。
var (
	binanceOrderUpdateConsumed   = []string{"i", "c", "X", "z", "ap", "L", "n", "N"}
	binanceAccountUpdateConsumed = []string{"m", "B", "a", "wb", "P", "s", "pa", "ep", "iw", "ps", "up"} // D-477 S1：+up→Profit
	binanceExecReportConsumed    = []string{"i", "c", "X", "z", "L", "n", "N"}
	binanceSpotAcctPosConsumed   = []string{"B", "a", "f", "l"}
)

// onSpotAccountPosition 见 binance_user_ws_balance.go（D-542 析出控行数）。
