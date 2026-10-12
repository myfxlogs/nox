// binance_user_ws_listenkey.go —— Binance 用户流 listenKey 管理面（自 binance_user_ws.go 折分，D-120 行数门禁）。
// keyReq/obtainListenKey/renewListenKey：POST 取 key → PUT 每 30min 续租 → 失效重取（不跨重连复用）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// keyReq listenKey REST 调用（X-MBX-APIKEY 头；client 按域收敛——D-106 原生域裸客户端）。
func (b *BinanceUserWS) keyReq(ctx context.Context, method, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-MBX-APIKEY", b.apiKey)
	resp, err := b.restFor(endpoint).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("%s %s: http %d: %s", method, endpoint, resp.StatusCode, truncateStr(string(data), 200))
	}
	return nil
}

func (b *BinanceUserWS) obtainListenKey(ctx context.Context) (string, error) {
	ep := b.listenKeyEndpoint()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("X-MBX-APIKEY", b.apiKey)
	resp, err := b.restFor(ep).Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("POST %s: http %d: %s", ep, resp.StatusCode, truncateStr(string(data), 200))
	}
	var r struct {
		ListenKey string `json:"listenKey"`
	}
	if err := json.Unmarshal(data, &r); err != nil || r.ListenKey == "" {
		return "", fmt.Errorf("listenKey response invalid: %s", truncateStr(string(data), 200))
	}
	return r.ListenKey, nil
}

// renewListenKey PUT 续租（fapi 同端点隐式续当前 key；spot 需 query listenKey=）。
func (b *BinanceUserWS) renewListenKey(ctx context.Context, key string) error {
	ep := b.listenKeyEndpoint()
	if b.venueType == "spot" {
		ep += "?listenKey=" + key
	}
	return b.keyReq(ctx, http.MethodPut, ep)
}
