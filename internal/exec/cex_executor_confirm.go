// cex_executor_confirm.go —— D-421 §1.2/§1.3：CEX 成交确认三态。
// 订单已受理但响应无成交数据 → 指数退避轮询 ≥3s；窗尽 cancel+终查双证定终态；
// cancel 也失败/仍 live → ErrFillUnconfirmed（绝不假 FAILED 假未成交）。
package exec

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// fillConfirmMaxWait 成交确认轮询总窗（D-421 ≥3s；起步 100ms 指数退避）。
const fillConfirmMaxWait = 3 * time.Second

// OrderStatusQuerier D-421 §1.5：按订单查证 venue 订单终态（消解器+收编前置守卫用）。
// 订单级查询（ordId/clOrdId），与持仓级匹配严格分层（KB cex-position-order-identity-mismatch）。
// state 归一值：filled / partial / canceled / live；accFillQty 已转基础币。
type OrderStatusQuerier interface {
	QueryOrderStatus(ctx context.Context, orderID, clOrdID, symbol string) (state string, accFillQty, avgPx, feeUSD float64, err error)
}

// QueryOrderStatus CEXExecutor 实现 OrderStatusQuerier（okx/binance perp+spot）。
func (e *CEXExecutor) QueryOrderStatus(ctx context.Context, orderID, clOrdID, symbol string) (string, float64, float64, float64, error) {
	switch e.venue {
	case "okx":
		st, err := e.okxOrderStatus(ctx, symbol, orderID, clOrdID)
		if err != nil {
			return "", 0, 0, 0, err
		}
		qty, _ := strconv.ParseFloat(st.AccFillSz, 64)
		if e.venueType == "perp" && qty > 0 {
			ctVal, _, cerr := e.okxContractVal(ctx, symbol)
			if cerr != nil {
				return "", 0, 0, 0, fmt.Errorf("okx ctval %s: %w", symbol, cerr)
			}
			qty *= ctVal // KB ctval-qty-conversion：张数→基础币
		}
		px, _ := strconv.ParseFloat(st.AvgPx, 64)
		fee, _ := okxFeeToUSD(st.Fee, st.FeeCcy)
		state := "live"
		switch st.State {
		case "filled":
			state = "filled"
		case "partially_filled":
			state = "partial"
		case "canceled", "rejected", "mmp_canceled":
			state = "canceled"
		}
		return state, qty, px, fee, nil
	case "binance":
		st, err := e.binanceOrderStatus(ctx, symbol, orderID, clOrdID)
		if err != nil {
			return "", 0, 0, 0, err
		}
		qty, _ := strconv.ParseFloat(st.ExecutedQty, 64)
		px, _ := strconv.ParseFloat(st.AvgPrice, 64)
		state := "live"
		switch st.Status {
		case "FILLED":
			state = "filled"
		case "PARTIALLY_FILLED":
			state = "partial"
		case "CANCELED", "REJECTED", "EXPIRED":
			state = "canceled"
		}
		return state, qty, px, 0, nil
	}
	return "", 0, 0, 0, fmt.Errorf("order status query unsupported venue %s", e.venue)
}

// evalOKXStatus 单次查单结果求值：filled→Fill；canceled/rejected→终态 err；否则 done=false 继续。
func (e *CEXExecutor) evalOKXStatus(origOrder Order, ordID string, st okxOrderStatusResult, ctVal float64) (Fill, bool, error) {
	switch st.State {
	case "canceled", "rejected", "mmp_canceled":
		// 订单未成交即被取消/拒绝 → 平仓失败，如实报错而非返回 qty=0 假装成交
		// （D-059 实测：spot SELL 曾因模拟盘撮合取消，旧逻辑静默返回 0 致测试假绿）。
		// cancelSourceReason 携带取消根因（如「滑超 best bid 5% 保护」），生产排障必读。
		reason := strings.TrimSpace(st.CancelSourceReason)
		if reason != "" {
			reason = ": " + reason
		}
		msg := fmt.Sprintf("okx order %s: state=%s accFillSz=%s avgPx=%s（未成交，订单被取消/拒绝%s）",
			ordID, st.State, st.AccFillSz, st.AvgPx, reason)
		e.alertCritical(origOrder, msg)
		return Fill{}, true, fmt.Errorf("%s", msg)
	}
	if st.AccFillSz == "" || st.AvgPx == "" {
		return Fill{}, false, nil
	}
	qty, _ := strconv.ParseFloat(st.AccFillSz, 64)
	px, _ := strconv.ParseFloat(st.AvgPx, 64)
	if qty <= 0 || px <= 0 {
		return Fill{}, false, nil
	}
	if e.venueType == "perp" { // D-125：accFillSz 张数 → 基础币
		qty *= ctVal
	}
	feeUSD, _ := okxFeeToUSD(st.Fee, st.FeeCcy) // D-391 S2：查单响应累计 fee
	return Fill{OrderID: ordID, Price: px, Qty: qty, FeeUSD: feeUSD, Ts: time.Now()}, true, nil
}

// okxConfirmFill D-421 §1.2：受理未确认的 OKX 单——退避轮询 ≥3s，窗尽 cancel+终查，
// 仍不可判 → ErrFillUnconfirmed（带 OrderID，消解凭据）。
func (e *CEXExecutor) okxConfirmFill(ctx context.Context, o Order, instID, ordID string, ctVal float64) (Fill, error) {
	deadline := time.Now().Add(fillConfirmMaxWait)
	delay := 100 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return Fill{}, ctx.Err()
		case <-time.After(delay):
		}
		st, err := e.okxOrderStatus(ctx, instID, ordID, "")
		if err == nil {
			if f, done, ferr := e.evalOKXStatus(o, ordID, st, ctVal); done {
				return f, ferr
			}
		}
		// 窗内查单网络错（非 ctx 取消）→ 继续轮询（受理单仍在簿上，断线≠未成交）。
		if delay *= 2; delay > 800*time.Millisecond {
			delay = 800 * time.Millisecond
		}
		if time.Now().Add(delay).After(deadline) {
			break
		}
	}
	// 窗尽 → cancel-order 后终查（cancel 成功/「已成交」报错均为确定性证据；
	// 错误文案因 venue 而异不依赖串匹配——终查 accFillSz/avgPx 是唯一事实源）。
	// D-508：cancel/终查失败 → critical（订单可能孤儿在簿）。
	if cerr := e.okxCancelOrder(ctx, instID, ordID); cerr != nil {
		e.alertCritical(o, fmt.Sprintf("okx cancel %s post-window failed: %v（订单可能孤儿在簿）", ordID, cerr))
	}
	st, err := e.okxOrderStatus(ctx, instID, ordID, "")
	if err != nil {
		e.alertCritical(o, fmt.Sprintf("okx order status final check %s failed: %v（订单可能孤儿在簿）", ordID, err))
		return Fill{}, &ErrFillUnconfirmed{OrderID: ordID}
	}
	if f, done, ferr := e.evalOKXStatus(o, ordID, st, ctVal); done {
		return f, ferr
	}
	return Fill{}, &ErrFillUnconfirmed{OrderID: ordID}
}

// evalBinanceStatus 单次查单结果求值（D-421 §1.3）：FILLED→Fill；
// CANCELED/REJECTED/EXPIRED→终态 err（executedQty>0 按真实部分量如实出 Fill）；
// PARTIALLY_FILLED/NEW 等 → done=false 继续轮询。
func (e *CEXExecutor) evalBinanceStatus(ctx context.Context, o Order, ordID string, st binanceOrderStatusResult) (Fill, bool, error) {
	q, _ := strconv.ParseFloat(st.ExecutedQty, 64)
	p, _ := strconv.ParseFloat(st.AvgPrice, 64)
	fee := func() float64 { // D-427 I3：userTrades 实费聚合；查询失败→0 诚实缺省不阻断
		f, err := e.binanceUserTradesFeeUSD(ctx, o.Symbol, ordID)
		if err != nil {
			return 0
		}
		return f
	}
	switch st.Status {
	case "CANCELED", "REJECTED", "EXPIRED":
		if q > 0 && p > 0 {
			// 取消但已有部分成交 → 真实敞口如实入账（audit P2-3：少记=平仓少平=venue 残余）。
			return Fill{OrderID: ordID, Price: p, Qty: q, Ts: time.Now(), FeeUSD: fee()}, true, nil
		}
		msg := fmt.Sprintf("binance order %s: status=%s executedQty=%s（未成交，订单被取消/拒绝）",
			ordID, st.Status, st.ExecutedQty)
		e.alertCritical(o, msg)
		return Fill{}, true, fmt.Errorf("%s", msg)
	case "FILLED":
		if q > 0 && p > 0 {
			return Fill{OrderID: ordID, Price: p, Qty: q, Ts: time.Now(), FeeUSD: fee()}, true, nil
		}
	}
	return Fill{}, false, nil
}

// binanceConfirmFill D-421 §1.3：受理未确认的 Binance perp 单——退避轮询至 FILLED，
// 窗尽 cancel+终查（executedQty>0=真实部分量；=0=确定未成交）；仍不可判 → ErrFillUnconfirmed。
func (e *CEXExecutor) binanceConfirmFill(ctx context.Context, o Order, ordID string) (Fill, error) {
	deadline := time.Now().Add(fillConfirmMaxWait)
	delay := 100 * time.Millisecond
	for {
		select {
		case <-ctx.Done():
			return Fill{}, ctx.Err()
		case <-time.After(delay):
		}
		st, err := e.binanceOrderStatus(ctx, o.Symbol, ordID, "")
		if err == nil {
			if f, done, ferr := e.evalBinanceStatus(ctx, o, ordID, st); done {
				return f, ferr
			}
		}
		if delay *= 2; delay > 800*time.Millisecond {
			delay = 800 * time.Millisecond
		}
		if time.Now().Add(delay).After(deadline) {
			break
		}
	}
	if cerr := e.binanceCancelOrder(ctx, o.Symbol, ordID); cerr != nil {
		e.alertCritical(o, fmt.Sprintf("binance cancel %s post-window failed: %v（订单可能孤儿在簿）", ordID, cerr))
	}
	st, err := e.binanceOrderStatus(ctx, o.Symbol, ordID, "")
	if err != nil {
		e.alertCritical(o, fmt.Sprintf("binance order status final check %s failed: %v（订单可能孤儿在簿）", ordID, err))
		return Fill{}, &ErrFillUnconfirmed{OrderID: ordID}
	}
	if f, done, ferr := e.evalBinanceStatus(ctx, o, ordID, st); done {
		return f, ferr
	}
	return Fill{}, &ErrFillUnconfirmed{OrderID: ordID}
}
