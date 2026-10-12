// okx_private_ws_events.go —— D-469a S1：OKX 私有流四频道帧分派与缓存/sink 落点
// （自 okx_private_ws.go 拆出，保 check-lines <300；account 行映射行为零变更）。
package exchange

import (
	"context"
	"encoding/json"
	"log"
	"strconv"
	"strings"
	"time"

	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
	"arb/internal/venue"
)

// okxVenueType 审计行 venue_type：venueKey "okx:perp"/"okx:perp#3" 后缀段；无 → unified。
func (o *OKXPrivateWS) okxVenueType() string {
	k := o.venueKey
	if i := strings.LastIndexByte(k, '#'); i >= 0 { // #<configID> 撞键后缀非键域（D-362）
		k = k[:i]
	}
	if t := keydomain.Parse(k).Type; t != "" {
		return t
	}
	return "unified"
}

// handleData case "" 数据推送分派（S0 实证：positions/account 帧顶层 eventType，
// balance_and_position 的 eventType 在 data[i] 元素内）。
func (o *OKXPrivateWS) handleData(ctx context.Context, channel string, raw []byte, data []map[string]any) {
	switch channel {
	case "account":
		if len(data) == 0 {
			return
		}
		row, avail, ok := okxAccountPushRow(data[0], o.now())
		if !ok {
			return
		}
		o.balLedger.Put("okx:unified", avail, row.EquityRaw, "USDT", "ws", row.FetchedAt) // D-542 S1
		if err := o.sink.InsertVenueAccountSnapshots(ctx, []pgstore.VenueAccountSnapshot{row}); err != nil {
			log.Printf("[okx-private-ws] insert snapshot: %v", err) // 写失败不杀读循环，下条推送重试
		}
		if o.hooks.OnAccountPush != nil {
			o.hooks.OnAccountPush()
		}
	case "positions":
		o.onAudit(ctx, "positions", raw)
		o.onPositions(data)
	case "orders":
		o.onAudit(ctx, "orders", raw)
		o.onOrders(ctx, data)
	case "balance_and_position":
		o.onAudit(ctx, "balance_and_position", raw)
		o.onBalPos(data)
	case "liquidation-warning":
		o.onAudit(ctx, "liquidation-warning", raw)
		o.onLiqWarning(data)
	}
}

// onAudit 帧落审计表（binance onAudit 同型；events nil 安全）。
func (o *OKXPrivateWS) onAudit(ctx context.Context, channel string, payload []byte) {
	if o.events == nil || len(payload) == 0 {
		return
	}
	if err := o.events.InsertCEXPrivateEvent(ctx, "okx", o.okxVenueType(), channel, "", payload); err != nil {
		log.Printf("[okx-private-ws] audit insert %s: %v (cache update proceeds)", channel, err)
	}
}

// onPositions positions 频道 → 持仓全量快照替换缓存（帧 data[] 即全量；
// 字段映射与 REST accountOKX positions 同口径：pos=张数原值、posSide→side、imr→margin 回落）。
func (o *OKXPrivateWS) onPositions(data []map[string]any) {
	now := o.now()
	orders := make([]venue.Order, 0, len(data))
	for _, it := range data {
		pos, perr := f64z(str(it["pos"]))
		if perr != nil {
			log.Printf("[okx-ws] drop field pos: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		if pos == 0 {
			// D-015 ②（R1）：pos=0 行=该腿已平（手动/强平/ADL 同形态）→ recon.Kick 叫醒对账。
			if o.hooks.OnPositionClosed != nil {
				o.hooks.OnPositionClosed(o.venueKey, str(it["instId"]), str(it["posSide"]))
			}
			continue
		}
		avgPx, perr := f64z(str(it["avgPx"]))
		if perr != nil {
			log.Printf("[okx-ws] drop field avgPx: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		upl, perr := f64z(str(it["upl"]))
		if perr != nil {
			log.Printf("[okx-ws] drop field upl: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		} // 浮盈（REST querier 同字段同语义）
		fee, perr := f64z(str(it["fee"]))
		if perr != nil {
			log.Printf("[okx-ws] drop field fee: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		} // 累计手续费（负=成本，同 REST）
		imr, perr := f64z(str(it["imr"]))
		if perr != nil {
			log.Printf("[okx-ws] drop field imr: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		if imr == 0 {
			if pv, perr := f64z(str(it["margin"])); perr != nil {
				log.Printf("[okx-ws] drop field margin: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
				return
			} else {
				imr = pv
			}
		}
		mmr, perr := f64z(str(it["mmr"]))
		if perr != nil {
			log.Printf("[okx-ws] drop field mmr: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		side := "BUY"
		if str(it["posSide"]) == "short" {
			side = "SELL"
		}
		// sim 环境 imr 实证为恒定假值（REST querier 同口径置 0 宁缺毋假）。
		if o.simulated {
			imr = 0
		}
		openedAt := now
		if ctimeMs, _ := strconv.ParseInt(str(it["cTime"]), 10, 64); ctimeMs > 0 {
			openedAt = time.UnixMilli(ctimeMs) // REST querier 同口径：cTime=开仓时刻
		}
		orders = append(orders, venue.Order{
			Ticket: str(it["instId"]), Symbol: str(it["instId"]), Side: side,
			Volume: pos, OpenPrice: avgPx, Profit: upl, Commission: fee,
			Venue: "okx", VenueKey: o.venueKey, PosSide: str(it["posSide"]),
			InitialMargin: imr, MaintMargin: mmr, OpenedAt: openedAt,
		})
	}
	if o.posCache != nil {
		o.posCache.PutPositions(o.venueKey, orders, now)
	}
	// D-475b S2：未消费关键字段观测（raw 重组——data 已解析）。
	if raw, err := json.Marshal(data); err == nil {
		WarnUnconsumed("okx", "positions", raw, okxPositionsConsumed)
	}
}

// onOrders orders 频道 → 订单事件缓存（state 归一 exec.OrderStatusQuerier 同域；
// accFillSz 张数→基础币须 ctVal（SWAP），查 instruments 公共端点缓存）。
func (o *OKXPrivateWS) onOrders(ctx context.Context, data []map[string]any) {
	for _, it := range data {
		state := "live"
		switch str(it["state"]) {
		case "filled":
			state = "filled"
		case "partially_filled":
			state = "partial"
		case "canceled", "mmp_canceled":
			state = "canceled"
		}
		qty, perr := f64z(str(it["accFillSz"]))
		if perr != nil {
			log.Printf("[okx-ws] drop field accFillSz: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		px, perr := f64z(str(it["avgPx"]))
		if perr != nil {
			log.Printf("[okx-ws] drop field avgPx: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		fee, perr := f64z(str(it["fee"]))
		if perr != nil {
			log.Printf("[okx-ws] drop field fee: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
			return
		}
		feeUSD := 0.0
		if ccy := str(it["feeCcy"]); fee != 0 && (ccy == "USDT" || ccy == "USD" || ccy == "USDC") {
			feeUSD = -fee // okx fee 负值=支出（REST okxFeeToUSD 同口径取绝对值）
			if feeUSD < 0 {
				feeUSD = -feeUSD
			}
		}
		instID := str(it["instId"])
		// D-477 S3（GAP-B4）：ctVal 解析器注入式单源（构造点 SetCtValFor，与 executor 同
		// baseURL 源）；nil=跳过换算保持张数+warn（禁硬编码第二域名源）。
		// D-566 包B 现态修正③：SPOT instId（如 BTC-USDT）accFillSz 原生即基础币，
		// ctValFor 只查 SWAP instruments 端点必报错→每帧 warn 噪音；非 -SWAP 后缀直接跳过。
		if o.ctValFor != nil && strings.HasSuffix(instID, "-SWAP") {
			if ctVal, err := o.ctValFor(ctx, instID); err == nil && ctVal > 0 {
				qty *= ctVal
			} else if err != nil {
				log.Printf("[okx-private-ws] orders ctVal %s: %v（accFillQty 以张计降级）", instID, err)
			}
		} else {
			log.Printf("[okx-private-ws] orders ctVal %s: ctValFor 未注入（accFillQty 以张计降级，GAP-B4 单源化待装配）", instID)
		}
		if o.orderCache != nil {
			o.orderCache.PutOrder(o.venueKey, str(it["ordId"]), str(it["clOrdId"]), OrderEvent{
				State: state, AccFillQty: qty, AvgPx: px, FeeUSD: feeUSD, At: o.now(),
			})
		}
	}
	if o.hooks.OnOrderPush != nil {
		o.hooks.OnOrderPush()
	}
	if raw, err := json.Marshal(data); err == nil {
		WarnUnconsumed("okx", "orders", raw, okxOrdersConsumed)
	}
}

// onBalPos balance_and_position → data[i].eventType 分派：funding_fee→kick；
// 其余 eventType（snapshot/delivered/transferred 等）已落审计表不另动作。
func (o *OKXPrivateWS) onBalPos(data []map[string]any) {
	for _, it := range data {
		if str(it["eventType"]) == "funding_fee" && o.hooks.OnFundingFee != nil {
			o.hooks.OnFundingFee()
		}
	}
	if raw, err := json.Marshal(data); err == nil {
		WarnUnconsumed("okx", "balance_and_position", raw, okxBalPosConsumed)
	}
}

// D-475b S2：各通道 consumed 键名清单（函数级 const——对拍契约的「已消费」声明，
// 与契约测试 skipFields 对偶：两边都应是显式清单而非隐式事实）。
var (
	// D-477 S4 B6：pnl（realized+unrealized 合并，已有 upl 覆盖浮盈）/liqPx/lever 在
	// venue.Order 无宿主字段 → ack 不建模（生产真帧实证 9-25）；新键仍会告警。
	okxPositionsConsumed = []string{"instId", "pos", "posSide", "avgPx", "upl", "fee", "imr", "margin", "mmr", "cTime", "pnl", "liqPx", "lever"}
	okxOrdersConsumed    = []string{"instId", "ordId", "clOrdId", "state", "accFillSz", "avgPx", "fee", "feeCcy"}
	okxBalPosConsumed    = []string{"eventType"}
)

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// okxAccountPushRow account channel 推送 → 快照行（纯函数）。
// 口径=REST accountOKX 同源（D-364 S2）：equity=Σ details[USDT].eqUsd（解析失败跳该币），
// balance=equity，currency=USDT，free_margin 不采（REST 同缺省）；details 无 USDT 行 →
// eq=0 行照落（同 REST 不 panic 语义）。
// D-542 S1：返回 avail=Σdetails[USDT].availBal（活账本可用源；解析失败跳该币同 eq 口径）。
func okxAccountPushRow(data map[string]any, now time.Time) (pgstore.VenueAccountSnapshot, float64, bool) {
	raw, err := json.Marshal(data)
	if err != nil {
		return pgstore.VenueAccountSnapshot{}, 0, false
	}
	var d struct {
		MgnRatio string                   `json:"mgnRatio"` // D-405 S2：账户保证金率（百分数，越高越安全）
		Details  []venue.OKXBalanceDetail `json:"details"`  // D-434 R1：与 REST 同构 schema
	}
	if err := json.Unmarshal(raw, &d); err != nil {
		return pgstore.VenueAccountSnapshot{}, 0, false
	}
	eq := 0.0
	avail := 0.0
	for _, det := range d.Details {
		if det.Ccy != "USDT" {
			continue
		}
		if usd, perr := strconv.ParseFloat(det.EqUsd, 64); perr == nil {
			eq += usd
		}
		if a, perr := strconv.ParseFloat(det.AvailBal, 64); perr == nil {
			avail += a // USDT 1:1（REST calibration 同口径）
		}
	}
	row := pgstore.VenueAccountSnapshot{
		Venue: "okx", VenueType: "unified",
		BalanceRaw: eq, EquityRaw: eq,
		Currency: "USDT", FetchedAt: now, Src: "okx_event",
	}
	// D-434 R1：buffer_multiple=与 REST accountOKX 同一共享函数三层回落（mmr→detail
	// mgnRatio→顶层 mgnRatio；本账户口径顶层恒空，WS 推送 details 实供 mmr/mgnRatio）。
	// <=0=未采 nil 语义保留（guard 诚实 data_stale）。
	if b := venue.OKXBufferMultiple(eq, d.Details, d.MgnRatio); b > 0 {
		row.BufferMultiple = &b
	}
	return row, avail, true
}
