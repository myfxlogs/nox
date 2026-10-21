// okx_ws_pulse.go —— 活性脉冲件（自 okx_ws.go 折分，行数门禁）。
package exchange

import "nox/internal/data/bus"

// handlePulse D-155：脉冲帧→pulseFn(canonical, venueKey)——mark-price 帧按
// SWAP 归 okx:perp，index-tickers 帧归 okx:spot（订阅面已定档不混）。data[]
// 逐项打点（聚合帧多项各刷各键）；pulseFn nil=静默跳过。
func (o *OKXWebSocket) handlePulse(msg wsMsg) {
	if o.pulseFn == nil {
		return
	}
	venueType := "spot"
	if msg.Arg.Channel == "mark-price" {
		venueType = "perp"
	}
	for _, it := range msg.Data {
		instID, _ := it["instId"].(string)
		if instID == "" {
			continue
		}
		o.pulseFn(bus.CexSymbolKey(instID), VenueOKX+":"+venueType)
	}
}
