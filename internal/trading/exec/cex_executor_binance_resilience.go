// cex_executor_binance_resilience.go —— D-569 包A S3-S5：Binance 请求时效韧性原语。
// httpStatusError 复用 D-566 定义（cex_executor_okx_resilience.go:19，同包共享禁重定义禁挪包）；
// binanceErrCode 码判定/-1021 delta 校时重发/GET 限频退避。镜像 okx_resilience 同构命名。
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sync/atomic"
	"time"
)

// binanceClockOffset 签名 ts 毫秒 delta（server-local；未校时=0=本地钟）。
// 校时伞盖域（R1 F-5）：仅 exec 包走 signedBinanceRequestOnce 的签名路径生效；
// reconcile/cex_io/cexscan 各有独立签名 ts 构造，不在本伞盖内。
var binanceClockOffset atomic.Int64

// Binance 错误分类表（设计 §2.2 钉死）：
//   - retryable：{-1003 限频, -1008 服务过载, -1001 断连} + HTTP 429；
//   - 终态（不重试直上抛）：{-2010 spot 余额不足, -2018 余额不足, -2019 保证金不足, -4105 族, -4400 族}；
//   - 特殊：-1021 → binanceClockOffset 校时后同 query 重签重发一次（再遇上抛防循环）；
//     HTTP 418 = IP 封禁递增不重试直接上抛；503 → 查单语义不重试。
func binanceRetryable(code int64, status int) bool {
	if status == http.StatusTooManyRequests {
		return true
	}
	if status == http.StatusTeapot {
		return false // 418：IP 封禁随重试递增，重试无意义
	}
	switch code {
	case -1003, -1008, -1001:
		return true
	}
	return false
}

// binanceErrCode 解 Binance 错误体 {"code":N,"msg":"…"}；非 httpStatusError/无 code → ok=false。
func binanceErrCode(err error) (code int64, ok bool) {
	var hse *httpStatusError
	if !errors.As(err, &hse) {
		return 0, false
	}
	var body struct {
		Code int64 `json:"code"`
	}
	if json.Unmarshal(hse.body, &body) != nil || body.Code == 0 {
		return 0, false
	}
	return body.Code, true
}

// binanceHTTPStatus 取类型化 status（非 httpStatusError → 0）。
func binanceHTTPStatus(err error) int {
	var hse *httpStatusError
	if errors.As(err, &hse) {
		return hse.status
	}
	return 0
}

// signedBinanceRequestRaw 签名请求外壳：单发走 signedBinanceRequestOnce；
// -1021（时钟偏移）→ binanceSyncClock 一次 → 同 params 新 delta 重签重发一次（签名纪律=同 query
// 串重算 timestamp+signature，禁复用旧签）；再遇 -1021 原样上抛（一次为限防循环）；
// 校时端点自身失败 → 原错误上抛（fail-closed 不猜 delta）。
func (e *CEXExecutor) signedBinanceRequestRaw(ctx context.Context, method, endpoint string, params url.Values) ([]byte, error) {
	data, err := e.signedBinanceRequestOnce(ctx, method, endpoint, params)
	if err == nil {
		return data, nil
	}
	if code, ok := binanceErrCode(err); !ok || code != -1021 {
		return nil, err
	}
	if serr := e.binanceSyncClock(ctx); serr != nil {
		return nil, err
	}
	return e.signedBinanceRequestOnce(ctx, method, endpoint, params)
}

// binanceSyncClock GET 校时端点（公开无签名：perp base+/v1/time；spot base+/v3/time）→
// serverTime-localNowMs 存 binanceClockOffset，Raw 系全部签名 ts 构造自动受益。
func (e *CEXExecutor) binanceSyncClock(ctx context.Context) error {
	ep := "/v3/time"
	if e.venueType == "perp" {
		ep = "/v1/time"
	}
	body, err := e.get(ctx, e.baseURL+ep, nil)
	if err != nil {
		return err
	}
	var r struct {
		ServerTime int64 `json:"serverTime"`
	}
	if json.Unmarshal(body, &r) != nil || r.ServerTime == 0 {
		return errors.New("binance time parse: serverTime 缺")
	}
	binanceClockOffset.Store(r.ServerTime - time.Now().UnixMilli())
	return nil
}

// binanceOrderStatusResult GET /fapi/v1/order（或 base+/v3/order）单条订单状态。
type binanceOrderStatusResult struct {
	Status      string `json:"status"`
	ExecutedQty string `json:"executedQty"`
	AvgPrice    string `json:"avgPrice"`
}

// binanceOrderStatus 查询 Binance 订单成交（perp /fapi/v1/order；spot base+/v3/order，签名 GET）。
// orderId 优先，空则 origClientOrderId 兜底（D-421 消解器按 clOrdId 查单凭据）。
// retryable（-1003/-1008/-1001/429）退避 ≤2 次（250/500ms 有界）；418/非 retryable 即时出。
func (e *CEXExecutor) binanceOrderStatus(ctx context.Context, symbol, ordID, clOrdID string) (binanceOrderStatusResult, error) {
	endpoint := "/v3/order"
	if e.venueType == "perp" {
		endpoint = "/v1/order"
	}
	params := url.Values{}
	params.Set("symbol", symbol)
	if ordID != "" {
		params.Set("orderId", ordID)
	} else {
		params.Set("origClientOrderId", clOrdID)
	}
	var lastErr error
	for attempt := 0; attempt <= 2; attempt++ {
		if attempt > 0 {
			d := 250 * time.Millisecond
			if attempt == 2 {
				d = 500 * time.Millisecond
			}
			select {
			case <-ctx.Done():
				return binanceOrderStatusResult{}, ctx.Err()
			case <-time.After(d):
			}
		}
		body, err := e.signedBinanceRequest(ctx, "GET", endpoint, params)
		if err == nil {
			return binanceOrderStatusResult{
				Status:      asString(body["status"]),
				ExecutedQty: asString(body["executedQty"]),
				AvgPrice:    asString(body["avgPrice"]),
			}, nil
		}
		lastErr = err
		code, _ := binanceErrCode(err)
		if !binanceRetryable(code, binanceHTTPStatus(err)) {
			return binanceOrderStatusResult{}, err
		}
	}
	return binanceOrderStatusResult{}, lastErr
}
