// cex_executor_gate_resilience.go —— D-570 包A S2：Gate 时效韧性域
// （gateErrLabel 类型化/gateClockOffset 校时/X-Gate-Exptime/GET 退避；镜像
// cex_executor_okx_resilience.go/cex_executor_binance_resilience.go 命名同构）。
// httpStatusError 复用 D-566 定义零重定义（e.get/e.post 已类型化返回，自动受益）。
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"arb/internal/signutil"
)

// gateErrLabel D-570 包A S2b：errors.As 解 *httpStatusError → body {"label":"..."} 解析。
// 非类型化/无 label → ok=false（镜像 binanceErrCode 形态；禁 string Contains 文案匹配）。
func gateErrLabel(err error) (label string, ok bool) {
	var he *httpStatusError
	if !errors.As(err, &he) {
		return "", false
	}
	var body struct {
		Label string `json:"label"`
	}
	if json.Unmarshal(he.body, &body) != nil || body.Label == "" {
		return "", false
	}
	return body.Label, true
}

// gateClockOffset Gate 服务器钟差（秒 delta：server-now - local-now，gateSyncClock 写）。
// 伞盖域钉死=exec 包走 GateSign 的签名 ts 构造点全列（D-570 枚举，漏一=伞有洞）：
// cex_executor_gate.go 四处（gateOrderStatus/GateAccountBookFund/gateSpecFetch/gateContractSize；
// placeGateRaw 走 gateSignedPost 同盖）、cex_executor_gate_leverage.go :40/:81、
// cex_executor_gate_position.go :33/:85、cex_executor_margin_gate.go :17/:70、
// cex_executor_spot_cash.go:188（gate spot 余额签名点）。WS 订阅帧签名 ts 不在 exec 域，
// 不属本伞盖（domain 外文件不预动）。
var gateClockOffset atomic.Int64

// gateNow GateSign 时间戳：本地 Unix 秒 + 校时 delta。
func gateNow() int64 { return time.Now().Unix() + gateClockOffset.Load() }

// getResponseHeader 直发无签名 GET 读响应头（e.get 只回 body——校时要 X-In-Time；
// 内部 e.client.Do 直用不引入新 http.Client）。
func (e *CEXExecutor) getResponseHeader(ctx context.Context, url, name string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := e.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("GET %s: status=%d", url, resp.StatusCode)
	}
	return resp.Header.Get(name), nil
}

// gateSyncClock D-570 包A S2c：直发无签名 GET tickers 读 X-In-Time 响应头
// （微秒串）算 delta 写 gateClockOffset。头缺席/解析败 → error 上抛（fail-closed 不猜）。
func (e *CEXExecutor) gateSyncClock(ctx context.Context) error {
	url := e.baseURL + "/futures/usdt/tickers?contract=BTC_USDT"
	sent := time.Now().Unix()
	h, err := e.getResponseHeader(ctx, url, "X-In-Time")
	if err != nil {
		return fmt.Errorf("gate sync clock: %w", err)
	}
	us, err := strconv.ParseInt(h, 10, 64)
	if err != nil || us <= 0 {
		return fmt.Errorf("gate sync clock: X-In-Time=%q invalid", h)
	}
	delta := us/1e6 - sent
	gateClockOffset.Store(delta)
	return nil
}

// gateSignedPost D-570 包A S2a/S2d：GateSign 签名 POST（下单专用）。
// headers 增 X-Gate-Exptime=now+15s 毫秒（仅下单 POST；GET/撤单不加）。
// retryExpired=true 时：REQUEST_EXPIRED → gateSyncClock → 同 bodyStr/urlPath 新 ts
// 重签重发一次（再遇/校时败 → 原样上抛，POST 禁自动重试其他错误）。
func (e *CEXExecutor) gateSignedPost(ctx context.Context, endpoint, urlPath, bodyStr string, retryExpired bool) ([]byte, error) {
	send := func() ([]byte, error) {
		ts := gateNow()
		sign := signutil.GateSign(e.apiSecret, "POST", urlPath, "", bodyStr, ts)
		headers := http.Header{
			"KEY":            {e.apiKey},
			"Timestamp":      {strconv.FormatInt(ts, 10)},
			"SIGN":           {sign},
			"Content-Type":   {"application/json"},
			"X-Gate-Exptime": {strconv.FormatInt(time.Now().Add(15*time.Second).UnixMilli(), 10)},
		}
		return e.post(ctx, e.baseURL+endpoint, bodyStr, headers)
	}
	body, err := send()
	if err != nil && retryExpired {
		if lab, ok := gateErrLabel(err); ok && lab == "REQUEST_EXPIRED" {
			if serr := e.gateSyncClock(ctx); serr != nil {
				return nil, err // 校时失败 → 原错误上抛（fail-closed）
			}
			return send() // 一次为限；再 REQUEST_EXPIRED 原样上抛
		}
	}
	return body, err
}

// gateRetryable D-570 包A S2e：GET 退避白名单——label∈{TOO_MANY_REQUESTS,
// TOO_FAST（官方释义=提现域频率标签，防御性保留——R1 F-3）} 或 status==429。
func gateRetryable(label string, status int) bool {
	return label == "TOO_MANY_REQUESTS" || label == "TOO_FAST" || status == 429
}

// gate 错误分类表（D-570 包A S2e，判定走 gateErrLabel 类型化禁文案匹配）：
// 终态直抛={BALANCE_NOT_ENOUGH, FUTURES_BALANCE_NOT_ENOUGH, INVALID_PRECISION,
// AMOUNT_TOO_LITTLE, AMOUNT_TOO_MUCH, INVALID_CLIENT_ORDER_ID, ORDER_EXISTS}；
// 特殊={REQUEST_EXPIRED→gateSignedPost 校时重发一次}；POST 不自动重试。

// gateOrderStatus 查询 Gate 订单成交（GET /futures/usdt/orders/{order_id}，签名 GET）。
// D-570 包A S2e：限频白名单退避 ≤2 次（250ms→500ms，ctx.Done 感知）；非 retryable/成功即时出。
func (e *CEXExecutor) gateOrderStatus(ctx context.Context, orderID string) (gateOrderResponse, error) {
	ord, err := e.gateStatusOnce(ctx, orderID)
	for _, delay := range []time.Duration{250 * time.Millisecond, 500 * time.Millisecond} {
		if err == nil {
			return ord, nil
		}
		lab, _ := gateErrLabel(err)
		var he *httpStatusError
		status := 0
		if errors.As(err, &he) {
			status = he.status
		}
		if !gateRetryable(lab, status) {
			return gateOrderResponse{}, err
		}
		select {
		case <-ctx.Done():
			return gateOrderResponse{}, ctx.Err()
		case <-time.After(delay):
		}
		ord, err = e.gateStatusOnce(ctx, orderID)
	}
	return ord, err
}

// gateStatusOnce gateOrderStatus 单发（签名 GET；退避包在外层 gateOrderStatus 循环）。
func (e *CEXExecutor) gateStatusOnce(ctx context.Context, orderID string) (gateOrderResponse, error) {
	endpoint := "/futures/usdt/orders/" + orderID
	urlPath := "/api/v4" + endpoint
	ts := gateNow()
	sign := signutil.GateSign(e.apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {e.apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := e.get(ctx, e.baseURL+endpoint, headers)
	if err != nil {
		return gateOrderResponse{}, err
	}
	var ord gateOrderResponse
	if err := json.Unmarshal(body, &ord); err != nil {
		return gateOrderResponse{}, fmt.Errorf("gate order status json: %w", err)
	}
	return ord, nil
}
