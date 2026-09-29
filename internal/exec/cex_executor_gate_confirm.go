// cex_executor_gate_confirm.go —— D-570 包B S4/S5：Gate 确认链三分量
// （PeekOrder 桥接+gateConfirmFill 窗+窗尽撤单终查）+text 单号真实化。
// 镜像 binanceConfirmFill 形态（三所确认链分文件同构——confirm.go 279 行禁堆叠）。
// 异构点：gate 订单帧原生 fee 字段（快照 :4376 JSON number 实弹）→ 桥接终态费零 REST。
package exec

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"time"

	"arb/internal/signutil"
)

// GateOrderSnap D-570 包B S4③：私有 WS 订单事件快照（gate 帧原生 fee → FeeUSD 回填）。
type GateOrderSnap struct {
	State      string
	AccFillQty float64
	AvgPx      float64
	FeeUSD     float64
}

// SetGateOrderLookup 注入订单事件查口（OrderEventCache.PeekOrder 闭包；nil 安全——
// 未接线时确认循环纯 REST 兜底不变）。
func (e *CEXExecutor) SetGateOrderLookup(fn func(ordID string) (GateOrderSnap, bool)) {
	e.gateOrderLookup = fn
}

// gateSnapEval D-570 包B S4③：事件快照求值——filled+qty>0 → Fill（费=帧回填零 REST）；
// canceled → qty>0 部分 Fill / ==0 诚实终态错误；live/partial → done=false 交 REST 拍照。
func (e *CEXExecutor) gateSnapEval(o Order, ordID string, sn GateOrderSnap) (Fill, bool, error) {
	switch sn.State {
	case "filled":
		if sn.AccFillQty > 0 && sn.AvgPx > 0 {
			return Fill{OrderID: ordID, Price: sn.AvgPx, Qty: sn.AccFillQty, Ts: time.Now(), FeeUSD: sn.FeeUSD}, true, nil
		}
	case "canceled":
		if sn.AccFillQty > 0 && sn.AvgPx > 0 {
			return Fill{OrderID: ordID, Price: sn.AvgPx, Qty: sn.AccFillQty, Ts: time.Now(), FeeUSD: sn.FeeUSD}, true, nil
		}
		msg := fmt.Sprintf("gate 订单 %s 未成交（已被取消，成交量为零）", ordID)
		e.alertCritical(o, msg)
		return Fill{}, true, fmt.Errorf("%s", msg)
	}
	return Fill{}, false, nil
}

// gateQueryOrderStatus D-570 包B S4①：QueryOrderStatus 的 gate 分支实现
// （confirm.go 行数纪律——本文件落地；orderID 优先、空退 clOrdID）。
// qty=|size-left| 合约数 ×quanto→基础币；REST 无费字段（桥接路径补，fee=0 如实）。
func (e *CEXExecutor) gateQueryOrderStatus(ctx context.Context, orderID, clOrdID, symbol string) (string, float64, float64, float64, error) {
	st, err := e.gateOrderStatus(ctx, orderID, clOrdID)
	if err != nil {
		return "", 0, 0, 0, err
	}
	spec, err := e.gateSpecFetch(ctx, symbol)
	if err != nil {
		return "", 0, 0, 0, fmt.Errorf("gate status %s spec: %w", orderID, err)
	}
	qty := math.Abs(float64(st.Size-st.Left)) * spec.Quanto
	px, perr := f64z(st.FillPrice)
	if perr != nil {
		return "", 0, 0, 0, fmt.Errorf("gate status %s fill_price=%q: %w", orderID, st.FillPrice, perr)
	}
	state := "live" // 与 onOrders 归一化同域逐字（finished+fill→filled/finished+其他→canceled/open+部分→partial/open→live）
	switch st.Status {
	case "finished":
		if st.FinishAs == "filled" {
			state = "filled"
		} else {
			state = "canceled"
		}
	case "open":
		if math.Abs(float64(st.Left)) < math.Abs(float64(st.Size)) {
			state = "partial"
		}
	}
	return state, qty, px, 0, nil
}

// gateStatusEval REST 状态求值（与 onOrders 归一化同域逐字；qty=|size-left| 合约数，
// 调用方乘 quanto——换算责任钉死 gateConfirmFill 内部，调用点不再乘防双乘）。
func gateStatusEval(o gateOrderResponse) (state string, qtyContracts float64, done bool) {
	if o.Status != "finished" {
		return "live", math.Abs(float64(o.Size - o.Left)), false
	}
	if o.FinishAs == "filled" {
		return "filled", math.Abs(float64(o.Size - o.Left)), true
	}
	return "canceled", math.Abs(float64(o.Size - o.Left)), true
}

// gateConfirmFill D-570 包B S4②：受理未确认的 gate 单——3s 窗退避轮询（先查事件 lookup
// 再落 REST），窗尽 cancel+终查；仍不可判 → ErrFillUnconfirmed。
// 返回 Fill.Qty=基础币（内部乘 quanto，调用点禁再乘——钉死防双乘）。
func (e *CEXExecutor) gateConfirmFill(ctx context.Context, o Order, ordID string) (Fill, error) {
	spec, err := e.gateSpecFetch(ctx, o.Symbol)
	if err != nil {
		return Fill{}, fmt.Errorf("gate confirm spec %s: %w", o.Symbol, err)
	}
	restEval := func() (Fill, bool, error) {
		st, serr := e.gateOrderStatus(ctx, ordID, "")
		if serr != nil {
			return Fill{}, false, nil // REST 败下拍再试（窗内软错）
		}
		state, qc, done := gateStatusEval(st)
		if !done {
			return Fill{}, false, nil
		}
		px, perr := f64z(st.FillPrice)
		if perr != nil {
			return Fill{}, true, fmt.Errorf("gate status %s fill_price=%q: %w", ordID, st.FillPrice, perr)
		}
		qty := qc * spec.Quanto // 合约数→基础币
		if state == "filled" && qty > 0 && px > 0 {
			return Fill{OrderID: ordID, Price: px, Qty: qty, Ts: time.Now()}, true, nil
		}
		if qty > 0 && px > 0 {
			return Fill{OrderID: ordID, Price: px, Qty: qty, Ts: time.Now()}, true, nil // 部分成交如实
		}
		msg := fmt.Sprintf("gate 订单 %s 未成交（已终结，成交量为零）\n原始明细：status=%s finish_as=%s", ordID, st.Status, st.FinishAs)
		e.alertCritical(o, msg)
		return Fill{}, true, fmt.Errorf("%s", msg)
	}
	deadline := time.Now().Add(fillConfirmMaxWait)
	delay := 100 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return Fill{}, ctx.Err()
		case <-time.After(delay):
		}
		if e.gateOrderLookup != nil {
			if sn, ok := e.gateOrderLookup(ordID); ok {
				if f, done, ferr := e.gateSnapEval(o, ordID, sn); done {
					return f, ferr
				}
			}
		}
		if f, done, ferr := restEval(); done {
			return f, ferr
		}
		if delay *= 2; delay > 800*time.Millisecond {
			delay = 800 * time.Millisecond
		}
		if time.Now().Add(delay).After(deadline) {
			break
		}
	}
	// 窗尽 → 撤单+终查双证（同 binance 文案口径「订单可能孤儿在簿」）。
	if cerr := e.gateCancelOrder(ctx, ordID); cerr != nil {
		e.alertCritical(o, fmt.Sprintf("gate 取消订单 %s 确认超时后处理失败：%v（订单可能仍挂在交易所、状态未知，需人工核对）", ordID, cerr))
	}
	st, err := e.gateOrderStatus(ctx, ordID, "")
	if err != nil {
		e.alertCritical(o, fmt.Sprintf("gate 订单 %s 最终查证失败：%v（订单可能仍挂在交易所、状态未知，需人工核对）", ordID, err))
		return Fill{}, &ErrFillUnconfirmed{OrderID: ordID}
	}
	state, qc, done := gateStatusEval(st)
	if done {
		px, _ := f64z(st.FillPrice)
		if qty := qc * spec.Quanto; qty > 0 && px > 0 {
			return Fill{OrderID: ordID, Price: px, Qty: qty, Ts: time.Now()}, nil
		}
		if state == "canceled" {
			return Fill{}, fmt.Errorf("gate order %s: canceled 零成交（未成交）", ordID)
		}
	}
	return Fill{}, &ErrFillUnconfirmed{OrderID: ordID}
}

// gateCancelOrder 撤单 DELETE /futures/usdt/orders/{order_id}（GateSign+gateNow 伞盖；
// 已终态单 DELETE 报错由调用方记 warn 不阻断——D-570 包B 窗尽路径专用）。
func (e *CEXExecutor) gateCancelOrder(ctx context.Context, orderID string) error {
	endpoint := "/futures/usdt/orders/" + orderID
	urlPath := "/api/v4" + endpoint
	ts := gateNow()
	sign := signutil.GateSign(e.apiSecret, "DELETE", urlPath, "", "", ts)
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, e.baseURL+endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("KEY", e.apiKey)
	req.Header.Set("Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("SIGN", sign)
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var b [512]byte
		n, _ := resp.Body.Read(b[:])
		return &httpStatusError{status: resp.StatusCode, body: b[:n]}
	}
	return nil
}

// gateOrderText D-570 包B S5：text 单号真实化——o.ClientOrderID → "t-"+id 截 28 字节
// （generateClientOrderID 产出纯 [a-zA-Z0-9] ⊂ gate charset [0-9A-Za-z_.-]）；
// 空 → "t-arb"（现状兜底不伪造）。效果链：onOrders PutOrder clOrdID 键有真实值
// （消恒同值跨单覆盖）+ QueryOrderStatus clOrdID 退位查询键成立。
func gateOrderText(o Order) string {
	if o.ClientOrderID == "" {
		return "t-arb"
	}
	t := "t-" + o.ClientOrderID
	if len(t) > 28 {
		t = t[:28]
	}
	return t
}
