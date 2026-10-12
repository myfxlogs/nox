// binance_user_ws_spot_events.go —— Binance spot 用户流事件帧处理
// （自 binance_user_ws_events.go 拆出，保 check-lines <300；行为零变更）。
package exchange

import (
	"encoding/json"
	"log"
	"strconv"
)

// onSpotExecutionReport spot executionReport → 订单事件缓存。
// 同 perp 改 map 精确键提取——spot 帧大小写键对更密（官方 schema）：
// i(OrderId)/I(ExecutionId)、c(ClientOrderId)/C(原客户端单号)、x(执行类型)/X(状态)、
// z(累计成交量)/Z(累计报价额)、l(末次量)/L(末价)、n(金额)/N(币种)——tag 全数中招。
// 字段集按原 struct 平移不增删：i=订单号 c=客户端单号 X=状态 z=累计量 L=末价 n=金额。
func (b *BinanceUserWS) onSpotExecutionReport(data []byte) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(data, &m); err != nil {
		return
	}
	ordID, oerr := jnum(m, "i").Int64()
	if oerr != nil || ordID == 0 {
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
	px, perr := f64z(jstr(m, "L"))
	if perr != nil {
		log.Printf("[binance-ws] drop field L: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
		return
	}
	fee, perr := f64z(jstr(m, "n"))
	if perr != nil {
		log.Printf("[binance-ws] drop field n: %v", perr) // D-508 S2b：脏字段=整帧不采 fail-closed
		return
	}
	// 同 perp 口径——n 按 N 币种计价，非 USD 系不冒称 USD（REST 兜底接管）。
	feeUSD := 0.0
	if ccy := jstr(m, "N"); ccy == "USDT" || ccy == "USD" || ccy == "USDC" {
		feeUSD = fee
	}
	b.orderCache.PutOrder(b.venueKey, strconv.FormatInt(ordID, 10), jstr(m, "c"), OrderEvent{
		State: state, AccFillQty: qty, AvgPx: px, FeeUSD: feeUSD, At: b.now(),
	})
	WarnUnconsumed("binance", "executionReport", data, binanceExecReportConsumed)
}
