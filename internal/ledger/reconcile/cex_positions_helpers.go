// cex_positions_helpers.go — CEX perp 持仓查询器辅助函数（D-121）。
// 从 cex_positions.go 拆出：gateQuantoMultiplier + cexGet（纯机械移动，不改逻辑）。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"arb/internal/platform/signutil"
)

// gateQuantoMultiplier D-121：查 Gate 合约规格 quanto_multiplier（每张合约代表的基础币数量）。
// 失败返回 1.0（兜底，不阻断持仓查询）。
func gateQuantoMultiplier(ctx context.Context, client *http.Client, baseURL, apiKey, apiSecret, contract string) float64 {
	endpoint := "/futures/usdt/contracts/" + contract
	urlPath := "/api/v4" + endpoint
	ts := time.Now().Unix()
	sign := signutil.GateSign(apiSecret, "GET", urlPath, "", "", ts)
	headers := http.Header{
		"KEY":       {apiKey},
		"Timestamp": {strconv.FormatInt(ts, 10)},
		"SIGN":      {sign},
	}
	body, err := cexGet(ctx, client, baseURL+endpoint, headers)
	if err != nil {
		return 1.0 // 兜底
	}
	var spec struct {
		QuantoMultiplier string `json:"quanto_multiplier"`
	}
	if err := json.Unmarshal(body, &spec); err != nil {
		return 1.0
	}
	q, err := strconv.ParseFloat(spec.QuantoMultiplier, 64)
	if err != nil || q <= 0 {
		return 1.0
	}
	return q
}

// cexGet 发带签名头的 GET 请求并读回 body（非 200 → 错误带响应体）。
func cexGet(ctx context.Context, client *http.Client, url string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}
