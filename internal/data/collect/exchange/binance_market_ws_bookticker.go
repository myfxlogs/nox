// binance_market_ws_bookticker.go —— bookTicker 帧分流处理（自 binance_market_ws.go 拆出，
// 300 行门禁）。handleBookTicker 消费 parseBookTicker（spotinfo.go 同源解析层）；
// isSubscribeAck 供 runLoop OnFrame 分流订阅 ack/错误帧。
package exchange

import (
	"encoding/json"
	"fmt"
	"log"
	"time"

	"arb/internal/data/bus"
)

// 返回是否业务产出帧（parseBookTicker 成功→publish 报价=true；
// ack/无关帧=false 不续数据看门狗）。
func (b *BinanceMarketWS) handleBookTicker(data []byte, venueType string, publish func(bus.Quote)) bool {
	f, ok := parseBookTicker(data)
	if !ok {
		return false // 订阅 ack/错误帧/无关帧静默（ack 已在 isSubscribeAck 分流）
	}
	var bidF, askF float64
	fmt.Sscanf(f.B, "%f", &bidF)
	fmt.Sscanf(f.A, "%f", &askF)
	ts := b.now()
	if f.T > 0 {
		ts = time.UnixMilli(f.T)
	}
	publish(bus.Quote{
		Symbol: f.S, Venue: VenueBinance, VenueType: venueType, VenueSymbol: f.S,
		Bid: bidF, Ask: askF, Time: ts,
	})
	return true
}

// isSubscribeAck 订阅 ack/错误帧判定：{result:…,id} 形（数据帧无 result 字段）。
func isSubscribeAck(data []byte) bool {
	var probe struct {
		Result any   `json:"result"`
		Error  any   `json:"error"`
		ID     int64 `json:"id"`
	}
	if err := json.Unmarshal(data, &probe); err != nil {
		return false
	}
	if probe.Error != nil {
		log.Printf("[binance-mkt-ws] subscribe error frame: %s", string(data))
		return true
	}
	return probe.ID != 0
}
