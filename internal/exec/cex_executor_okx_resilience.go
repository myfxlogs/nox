// cex_executor_okx_resilience.go —— D-566 包A S3：OKX 请求时效韧性原语。
// httpStatusError（共享类型化错误，D-569 复用契约：禁改名禁挪包）/50102 校时/GET 限频退避。
package exec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// httpStatusError D-566 S3：e.get/e.post 非 2xx 的类型化错误（body 保留供错误码判定）。
// Error() 与旧 fmt.Errorf("http %d: %s") 消息逐字相同——全部既有调用方只格式化打印。
// D-569 共享协调点：后票直接 errors.As 复用，禁重复定义。
type httpStatusError struct {
	status int
	body   []byte
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("http %d: %s", e.status, strings.TrimSpace(string(e.body)))
}

// okxCodeFromBody 解响应体顶层 code（OKX 业务码在 body，与 HTTP status 分层）。
func okxCodeFromBody(b []byte) string {
	var r struct {
		Code string `json:"code"`
	}
	if json.Unmarshal(b, &r) != nil {
		return ""
	}
	return r.Code
}

// okxIs50102 时钟过期判定（官方 50102=HTTP 401 携带 body code，或 2xx body code=="50102"）。
// 禁错误文本 Contains（KB error-sentinel-unreachable-past-string-boundary）。
func okxIs50102(err error, body []byte) bool {
	var hse *httpStatusError
	switch {
	case err != nil && errors.As(err, &hse):
		return okxCodeFromBody(hse.body) == "50102"
	case err != nil:
		return false
	default:
		return okxCodeFromBody(body) == "50102"
	}
}

// okxRetryable GET 限频/系统繁忙码集合（50011/50013/50001/50004/51149）——venue 契约常量。
func okxRetryable(code string) bool {
	switch code {
	case "50011", "50013", "50001", "50004", "51149":
		return true
	}
	return false
}

// okxNowISO 校时感知的时间戳（clockDeltaMs=serverTs-localMs，atomic；未校时=本地钟）。
func (e *CEXExecutor) okxNowISO() string {
	return time.Now().Add(time.Duration(e.clockDeltaMs.Load()) * time.Millisecond).UTC().Format("2006-01-02T15:04:05.000Z")
}

// okxSyncClock GET /api/v5/public/time（公开无签名）→ delta=serverTs-localNowMs 存 atomic。
// 成功后进程内持续生效，全部签名 ts 构造点经 okxNowISO 自动受益。
func (e *CEXExecutor) okxSyncClock(ctx context.Context) error {
	body, err := e.get(ctx, e.baseURL+"/api/v5/public/time", nil)
	if err != nil {
		return fmt.Errorf("okx public/time: %w", err)
	}
	var r struct {
		Data []struct {
			TS string `json:"ts"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &r); err != nil || len(r.Data) == 0 {
		return fmt.Errorf("okx public/time parse: len=%d err=%v", len(r.Data), err)
	}
	srv, err := strconv.ParseInt(r.Data[0].TS, 10, 64)
	if err != nil {
		return fmt.Errorf("okx public/time ts=%q: %w", r.Data[0].TS, err)
	}
	e.clockDeltaMs.Store(srv - time.Now().UnixMilli())
	return nil
}

// okxGetSignedRetry D-566 S3：签名 GET 统一退避+50102 校时（okxOrderStatus/okxPosModeFetch
// 两热路径专用）。retryable 码退避 ≤2 次（250/500ms 有界，KB retry-storm-positive-feedback）；
// 50102 校时后重发不占退避额度且每次调用至多校时一次（再遇 50102 原样上抛防循环）；
// 网络层错误无 code 证据不属限频集 → 直接上抛。
func (e *CEXExecutor) okxGetSignedRetry(ctx context.Context, path string) ([]byte, error) {
	synced := false
	retry := 0
	for {
		ts := e.okxNowISO()
		headers := http.Header{
			"OK-ACCESS-KEY":        {e.apiKey},
			"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
			"OK-ACCESS-TIMESTAMP":  {ts},
			"OK-ACCESS-PASSPHRASE": {e.passphrase},
		}
		body, err := e.get(ctx, e.baseURL+path, headers)
		var code string
		if err != nil {
			var hse *httpStatusError
			if !errors.As(err, &hse) {
				return nil, err
			}
			code = okxCodeFromBody(hse.body)
		} else {
			code = okxCodeFromBody(body)
		}
		if code == "50102" && !synced {
			if serr := e.okxSyncClock(ctx); serr == nil {
				synced = true
				continue
			}
			return body, err
		}
		if err != nil && !okxRetryable(code) {
			return body, err
		}
		if !okxRetryable(code) {
			return body, nil // code=0 或非退避业务码 → 交上层解析
		}
		if retry >= 2 {
			return body, err // 退避耗尽：body 交上层 code!=0 报错 / err 原样上抛
		}
		d := 250 * time.Millisecond
		if retry == 1 {
			d = 500 * time.Millisecond
		}
		retry++
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(d):
		}
	}
}
