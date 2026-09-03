// D-211 TRX 采集器 HTTP 辅助（TronGrid API 请求）。
package trx

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// httpClient 可被测试替换（httptest.NewServer + SetHTTPClient）。
var httpClient = &http.Client{Timeout: 30 * time.Second}

// SetHTTPClient 替换默认 HTTP 客户端（测试用 httptest.Server）。
func SetHTTPClient(c *http.Client) { httpClient = c }

// tronPost 发送 TronGrid POST 请求（getaccount/listwitnesses 等）。
func tronPost(ctx context.Context, base, apiKey, path, body string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "POST", base+path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", apiKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tron POST %s: status %d", path, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// tronGet 发送 TronGrid GET 请求（/v1/transactions 等）。
func tronGet(ctx context.Context, base, apiKey, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	if apiKey != "" {
		req.Header.Set("TRON-PRO-API-KEY", apiKey)
	}
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tron GET: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}
