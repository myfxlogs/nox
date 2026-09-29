// cex_executor_okx_confirm.go —— D-566 包B S4：OKX 确认桥接件（PeekOrder 事件先行）。
// 镜像 cex_executor_gate_confirm.go 先例；OKX 事件帧原生 FeeUSD（onOrders 已解析 fee+feeCcy），
// 异于 binance 快照无费域——费零 REST 透传。
package exec

import (
	"fmt"
	"time"
)

// OKXOrderSnap 订单事件快照（State=OrderEvent 归一域 filled/partial/canceled/live；
// AccFillQty=基础币——events 层 SWAP 已×ctVal、SPOT 原生）。
type OKXOrderSnap struct {
	State      string
	AccFillQty float64
	AvgPx      float64
	FeeUSD     float64
}

// SetOKXOrderLookup 注入订单事件查口（OrderEventCache.PeekOrder 闭包；nil 安全——
// 未接线时确认循环纯 REST 兜底不变）。
func (e *CEXExecutor) SetOKXOrderLookup(fn func(ordID string) (OKXOrderSnap, bool)) {
	e.okxOrderLookup = fn
}

// okxSnapEval 事件快照求值（critical 文案对齐 evalOKXStatus 语义）：
// filled+qty>0+px>0 → Fill 含 FeeUSD；canceled → qty>0 部分 Fill / ==0 诚实错误；
// 其余（live/partial/字段残缺）→ done=false 交 REST 拍照兜底。
func (e *CEXExecutor) okxSnapEval(o Order, ordID string, sn OKXOrderSnap) (Fill, bool, error) {
	switch sn.State {
	case "filled":
		if sn.AccFillQty > 0 && sn.AvgPx > 0 {
			return Fill{OrderID: ordID, Price: sn.AvgPx, Qty: sn.AccFillQty, FeeUSD: sn.FeeUSD, Ts: time.Now()}, true, nil
		}
	case "canceled":
		if sn.AccFillQty > 0 && sn.AvgPx > 0 {
			return Fill{OrderID: ordID, Price: sn.AvgPx, Qty: sn.AccFillQty, FeeUSD: sn.FeeUSD, Ts: time.Now()}, true, nil
		}
		msg := fmt.Sprintf("okx order %s: state=canceled accFillSz=0（未成交）", ordID)
		e.alertCritical(o, msg)
		return Fill{}, true, fmt.Errorf("%s", msg)
	}
	return Fill{}, false, nil
}
