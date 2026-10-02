// cex_executor_maker_wait.go —— D-018 R1：post-only 成交等待（事件驱动，D-004 数据流正道）。
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
	"time"

	"arb/internal/signutil"
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
	var fill Fill
	switch e.venue {
	case "okx":
		if e.okxOrderLookup == nil {
			return Fill{}, false, nil
		}
		sn, ok := e.okxOrderLookup(orderID)
		if !ok {
			return Fill{}, false, nil
		}
		fill = Fill{OrderID: orderID, Price: sn.AvgPx, Qty: sn.AccFillQty, FeeUSD: sn.FeeUSD, Ts: time.Now(), FillKind: "maker"}
	case "binance":
		if e.binanceOrderLookup == nil {
			return Fill{}, false, nil
		}
		sn, ok := e.binanceOrderLookup(orderID)
		if !ok {
			return Fill{}, false, nil
		}
		fill = Fill{OrderID: orderID, Price: sn.AvgPx, Qty: sn.AccFillQty, FeeUSD: sn.FeeUSD, Ts: time.Now(), FillKind: "maker"}
	case "gate":
		if e.gateOrderLookup == nil {
			return Fill{}, false, nil
		}
		sn, ok := e.gateOrderLookup(orderID)
		if !ok {
			return Fill{}, false, nil
		}
		fill = Fill{OrderID: orderID, Price: sn.AvgPx, Qty: sn.AccFillQty, FeeUSD: sn.FeeUSD, Ts: time.Now(), FillKind: "maker"}
	}
	if fill.Qty <= 0 && fill.Price <= 0 {
		// 快照残缺（帧早于撮合回填）——视为未终态继续等。
		return Fill{}, false, nil
	}
	if fill.Qty > 0 {
		return fill, true, nil
	}
	// 键在而 qty=0=canceled 干净终态（快照键只在事件写时存在）。
	return Fill{OrderID: orderID, FillKind: "maker", Ts: time.Now()}, true, nil
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
