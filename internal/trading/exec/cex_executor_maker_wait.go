// cex_executor_maker_wait.go —— D-018：post-only 成交等待与撤单终查（事件驱动，D-004 数据流正道）。
// 唤醒源=OrderEventCache 订阅扇出（私有 WS 订单帧写缓存即拍 venueKey）；收到拍回读快照
// （PeekOrder 闭包→三所归一 eval，零 REST）终判。等待期间零 REST 查单；REST 只出现在
// 超时撤单终查一次（PostOnlyCancel）。未接线 orderWake=退化裸等 ctx（超时 T 撤单终查
// 兜底，语义不变、感知延迟变大=装配缺口）。
package exec

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"nox/internal/platform/signutil"
)

// SetOrderWake 注入订单事件订阅闭包（OrderEventCache.SubscribeEvents；manager 装配，
// nil 安全——未接线退化裸等）。
func (e *CEXExecutor) SetOrderWake(sub func() (<-chan string, func())) {
	e.orderWake = sub
}

// makerSnapFill maker 等待期快照终判（零 REST，PeekOrder 闭包→归一 eval）：
// filled → (Fill, true, nil)；canceled → (Fill[qty=已成交可 0], true, nil)——canceled 是
// maker 预期终态（撤单/被撤），与确认链 eval 不同：零成交不告警不报错；live/partial/
// 快照 miss → (zero, false, nil) 继续等。
func (e *CEXExecutor) makerSnapFill(o Order, orderID string) (Fill, bool, error) {
	var state string
	var qty, px, fee float64
	var have bool
	switch e.venue {
	case "okx":
		state, qty, px, fee, have = e.okxOrderLookupSafe3(orderID)
	case "binance":
		state, qty, px, fee, have = e.binanceOrderLookupSafe3(orderID)
	case "gate":
		state, qty, px, fee, have = e.gateOrderLookupSafe3(orderID)
	default:
		return Fill{}, false, nil
	}
	if !have {
		return Fill{}, false, nil // 快照 miss=帧未到，继续等
	}
	snap := OKXOrderSnap{State: state, AccFillQty: qty, AvgPx: px, FeeUSD: fee}
	switch snap.State {
	case "canceled":
		// 阶段b②：State 显式早退——qty=0 干净终态 / qty>0 撤前部分成交，
		// 均即刻终判（秒撤帧到达不等 T）。
		fill := Fill{OrderID: orderID, Price: snap.AvgPx, Qty: snap.AccFillQty, FeeUSD: snap.FeeUSD, Ts: time.Now(), FillKind: "maker"}
		return fill, true, nil
	case "filled":
		if snap.AccFillQty > 0 && snap.AvgPx > 0 {
			return Fill{OrderID: orderID, Price: snap.AvgPx, Qty: snap.AccFillQty, FeeUSD: snap.FeeUSD, Ts: time.Now(), FillKind: "maker"}, true, nil
		}
		return Fill{}, false, nil // 字段残缺（帧早于撮合回填）继续等
	default: // live / partial / 空态——未终态继续等
		return Fill{}, false, nil
	}
}

// okxOrderLookupSafe3 / binanceOrderLookupSafe3 / gateOrderLookupSafe3：lookup 空安全
// 包装（归一五元组 State/qty/px/fee/have——三所 snap 同构展开，零单位换算：events 层已归一基础币）。
func (e *CEXExecutor) okxOrderLookupSafe3(orderID string) (string, float64, float64, float64, bool) {
	if e.okxOrderLookup == nil {
		return "", 0, 0, 0, false
	}
	sn, ok := e.okxOrderLookup(orderID)
	return sn.State, sn.AccFillQty, sn.AvgPx, sn.FeeUSD, ok
}

func (e *CEXExecutor) binanceOrderLookupSafe3(orderID string) (string, float64, float64, float64, bool) {
	if e.binanceOrderLookup == nil {
		return "", 0, 0, 0, false
	}
	sn, ok := e.binanceOrderLookup(orderID)
	return sn.State, sn.AccFillQty, sn.AvgPx, sn.FeeUSD, ok
}

func (e *CEXExecutor) gateOrderLookupSafe3(orderID string) (string, float64, float64, float64, bool) {
	if e.gateOrderLookup == nil {
		return "", 0, 0, 0, false
	}
	sn, ok := e.gateOrderLookup(orderID)
	return sn.State, sn.AccFillQty, sn.AvgPx, sn.FeeUSD, ok
}

// AwaitPostOnlyFill 事件驱动等待 post-only 成交至终态或 ctx 到期（=超时 T）。
// 唤醒=OrderEventCache 订阅扇出（私有 WS 订单帧写缓存）；判定全走快照闭包（零 REST）。
// 返回 (fill, filled, err)：filled=true → fill 有效（必 maker）；ctx 到期 filled=false
// （订单可能仍 live——调用方必须 PostOnlyCancel 终查，禁直接当未成交）。
// 拍 channel 被取消方关闭/未接线 → 退化裸等 ctx（终查兜底不变）。
func (e *CEXExecutor) AwaitPostOnlyFill(ctx context.Context, o Order, orderID string) (Fill, bool, error) {
	var wake <-chan string
	var cancelSub func()
	if e.orderWake != nil {
		wake, cancelSub = e.orderWake()
		if cancelSub != nil {
			defer cancelSub()
		}
	}
	for {
		fill, done, serr := e.makerSnapFill(o, orderID)
		if serr != nil {
			return Fill{}, false, serr
		}
		if done {
			return fill, fill.Qty > 0, nil
		}
		if wake == nil {
			// 未接线/已关：裸等 ctx（PostOnlyCancel 终查兜底）。
			<-ctx.Done()
			return Fill{}, false, nil
		}
		select {
		case <-ctx.Done():
			return Fill{}, false, nil
		case _, ok := <-wake:
			if !ok {
				wake = nil // 订阅被取消方关闭 → 退化裸等
			}
		}
	}
}

// PostOnlyCancel 撤单并回查终态。返回 Fill=撤单时刻的累计成交（Qty=基础币；0=干净撤，
// 必 maker 口径）。撤单请求本身失败（网络/超时）=错误上抛——订单可能仍在挂/已成交，
// 上层按 UNKNOWN 语义处理（禁假设未成交）。
func (e *CEXExecutor) PostOnlyCancel(ctx context.Context, o Order, orderID string) (Fill, error) {
	switch e.venue {
	case "okx":
		instID := o.Symbol
		if !strings.HasSuffix(instID, "-SWAP") {
			instID = instID + "-SWAP"
		}
		if err := e.okxCancelOrder(ctx, instID, orderID); err != nil {
			return Fill{}, fmt.Errorf("okx cancel: %w", err)
		}
	case "binance":
		if err := e.binanceCancelOrder(ctx, o.Symbol, orderID); err != nil {
			// -2011=订单不存在/已终态——不视为撤单失败，落入下方终查判定真实状态。
			if !strings.Contains(err.Error(), "-2011") {
				return Fill{}, fmt.Errorf("binance cancel: %w", err)
			}
		}
	case "gate":
		endpoint := "/futures/usdt/orders/" + orderID
		if err := e.gateSignedDelete(ctx, endpoint, "/api/v4"+endpoint); err != nil {
			return Fill{}, fmt.Errorf("gate cancel: %w", err)
		}
	default:
		return Fill{}, fmt.Errorf("cancel: unsupported venue %q", e.venue)
	}
	return e.postOnlyCancelResult(ctx, o, orderID)
}

// postOnlyCancelResult D-018 阶段b③：撤后终查=事件唤醒优先（撤单令 WS canceled 帧
// 写缓存→订阅拍即刻回读快照零 REST 判定）+ 单次 REST 兜底（帧丢失/未接线）——
// 替代旧 200ms REST 循环（D-004 同源修正）。live/partial 仍非终态=错误上抛
// （禁假设未成交，fail-closed）。
func (e *CEXExecutor) postOnlyCancelResult(ctx context.Context, o Order, orderID string) (Fill, error) {
	// 事件唤醒窗（2s）：撤单帧通常 ms 级到达。
	wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	fill, done := e.awaitCancelSnap(wctx, o, orderID)
	if done {
		return fill, nil
	}
	// 单次 REST 兜底（帧丢失/orderWake 未接线）。
	state, qty, px, fee, err := e.QueryOrderStatus(ctx, orderID, o.ClientOrderID, o.Symbol)
	if err != nil {
		return Fill{}, fmt.Errorf("post-cancel status: %w", err)
	}
	switch state {
	case "canceled", "filled":
		return Fill{OrderID: orderID, Price: px, Qty: qty, FeeUSD: fee, FillKind: "maker", Ts: time.Now()}, nil
	}
	return Fill{}, fmt.Errorf("post-cancel status=%s 未达终态（撤单未生效？）fail-closed", state)
}

// awaitCancelSnap 撤后事件唤醒等待（纯事件，无节拍）：先查一次快照（撤单响应与帧写入
// 竞态窗）→ 订阅拍到达即回读终判。未接线/窗尽无终态=done=false 交单次 REST 兜底。
func (e *CEXExecutor) awaitCancelSnap(ctx context.Context, o Order, orderID string) (Fill, bool) {
	if e.orderWake == nil {
		return Fill{}, false
	}
	if fill, done, _ := e.makerSnapFill(o, orderID); done {
		return fill, true
	}
	wake, cancelSub := e.orderWake()
	defer cancelSub()
	for {
		select {
		case <-ctx.Done():
			return Fill{}, false
		case _, ok := <-wake:
			if !ok {
				return Fill{}, false
			}
			if fill, done, _ := e.makerSnapFill(o, orderID); done {
				return fill, true
			}
		}
	}
}

// gateSignedDelete gate v4 DELETE 签名请求（撤单；GateSign method="DELETE"、query/body=""）。
func (e *CEXExecutor) gateSignedDelete(ctx context.Context, endpoint, urlPath string) error {
	ts := gateNow()
	sign := signutil.GateSign(e.apiSecret, "DELETE", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
		"Accept":    {"application/json"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, e.baseURL+endpoint, nil)
	if err != nil {
		return err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("gate delete %s: http %d", endpoint, resp.StatusCode)
	}
	return nil
}
