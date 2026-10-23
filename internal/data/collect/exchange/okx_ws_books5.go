// okx_ws_books5.go —— books5 深度采集件（ws 主供 REST 断线兜底）。
package exchange

import (
	"context"
	"strconv"
	"time"
)

// CexDepthRowLite ws 侧深度行（InsertCexDepth 同形消费面——wire 期拼装）。
type CexDepthRowLite struct {
	Venue, Symbol, VenueSymbol string
	Ts                         time.Time
	Bid1USD, Ask1USD           float64
	Bid1Qty, Ask1Qty           float64
}

// SetDepthEmit 注入深度行发射口（wire 期先于 Run；nil 安全）。异步消费——
// 背压不连坐 tickers 读循环（满丢弃留痕）。
func (o *OKXWebSocket) SetDepthEmit(fn func(ctx context.Context, rows []CexDepthRowLite) error) {
	o.depthEmit = fn
}

// books5ThrottleMin 逐 instId 节流窗（120 行/s 级密帧同档帧率收敛）。
const books5ThrottleMin = 3 * time.Second

// handleBooks5 books5 帧→cex_depth 行（顶档价语义——Bid1USD/Ask1USD=顶档价非
// 名义量，返修④契约有据 LatestDepthHalfSpread 半价差消费面；Symbol=instID
// 原生形与 REST 写域同域〔键域错位坑修〕；mid×min(q)=名义深度列位）。
func (o *OKXWebSocket) handleBooks5(ctx context.Context, msg wsMsg) {
	if len(msg.Data) == 0 {
		return
	}
	instID, _ := msg.Arg.InstID.(string)
	if instID == "" {
		return
	}
	now := time.Now()
	o.depthMu.Lock()
	if ts, ok := o.depthLast[instID]; ok && now.Sub(ts) < books5ThrottleMin {
		o.depthMu.Unlock()
		return
	}
	o.depthLast[instID] = now
	o.depthMu.Unlock()
	it := msg.Data[0]
	bids, _ := it["bids"].([]any)
	asks, _ := it["asks"].([]any)
	if len(bids) == 0 || len(asks) == 0 {
		return
	}
	lvl := func(l any) (px, qty float64, ok bool) {
		arr, ok := l.([]any)
		if !ok || len(arr) < 2 {
			return 0, 0, false
		}
		pxs, ok1 := arr[0].(string)
		qts, ok2 := arr[1].(string)
		if !ok1 || !ok2 {
			return 0, 0, false
		}
		px, e1 := strconv.ParseFloat(pxs, 64)
		qty, e2 := strconv.ParseFloat(qts, 64)
		if e1 != nil || e2 != nil {
			return 0, 0, false
		}
		return px, qty, true
	}
	bidPx, bidQty, ok1 := lvl(bids[0])
	askPx, askQty, ok2 := lvl(asks[0])
	if !ok1 || !ok2 || bidPx == 0 || askPx == 0 {
		return
	}
	// 异步 emit：buf 满丢弃留痕——背压不连坐 tickers 读循环。
	row := CexDepthRowLite{
		Venue: VenueOKX, Symbol: instID, VenueSymbol: instID, Ts: now,
		Bid1USD: bidPx, Ask1USD: askPx, Bid1Qty: bidQty, Ask1Qty: askQty,
	}
	select {
	case o.depthQ <- row:
	default:
		o.depthDropped++
	}
}

// runDepthEmit 异步消费协程（深度队列清空非阻塞）。
func (o *OKXWebSocket) runDepthEmit(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case row := <-o.depthQ:
			if o.depthEmit != nil {
				_ = o.depthEmit(ctx, []CexDepthRowLite{row})
			}
		}
	}
}
