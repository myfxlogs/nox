// okx_ws_books5.go —— books5 深度采集件（D-667 族：ws 主供 REST 断线兜底）。
package exchange

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// handleBooks5 books5 帧→cex_depth 行（bid1/ask1 顶层+qty 双段USD 深度）。
// DepthEmitFn nil=不 emit（wire 期注入 InsertCexDepth 同形消费面）。
func (o *OKXWebSocket) handleBooks5(ctx context.Context, msg wsMsg) {
	if o.depthEmit == nil || len(msg.Data) == 0 {
		return
	}
	instID, _ := msg.Arg.InstID.(string)
	if instID == "" {
		return
	}
	it := msg.Data[0]
	bids, _ := it["bids"].([]any)
	asks, _ := it["asks"].([]any)
	if len(bids) == 0 || len(asks) == 0 {
		return
	}
	parseLvl := func(l any) (px, qty float64) {
		if arr, ok := l.([]any); ok && len(arr) >= 2 {
			fmt.Sscanf(arr[0].(string), "%f", &px)
			fmt.Sscanf(arr[1].(string), "%f", &qty)
		}
		return
	}
	bidPx, bidQty := parseLvl(bids[0])
	askPx, askQty := parseLvl(asks[0])
	if bidPx == 0 || askPx == 0 {
		return
	}
	canon := strings.ReplaceAll(strings.TrimSuffix(instID, "-SWAP"), "-", "")
	_ = o.depthEmit(ctx, []CexDepthRowLite{{
		Venue: VenueOKX, Symbol: canon, VenueSymbol: instID, Ts: time.Now(),
		Bid1USD: bidPx * bidQty, Ask1USD: askPx * askQty,
		Bid1Qty: bidQty, Ask1Qty: askQty,
	}})
}

// CexDepthRowLite ws 侧深度行（InsertCexDepth 同形消费面——wire 期拼装）。
type CexDepthRowLite struct {
	Venue, Symbol, VenueSymbol string
	Ts                         time.Time
	Bid1USD, Ask1USD           float64
	Bid1Qty, Ask1Qty           float64
}

// SetDepthEmit 注入深度行发射口（wire 期先于 Run；nil 安全）。
func (o *OKXWebSocket) SetDepthEmit(fn func(ctx context.Context, rows []CexDepthRowLite) error) {
	o.depthEmit = fn
}
