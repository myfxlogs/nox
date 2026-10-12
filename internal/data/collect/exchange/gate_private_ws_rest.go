// gate_private_ws_rest.go —— Gate 私有流 REST 支撑面（D-570 包C 自 gate_private_ws.go
// 拆出保 check-lines <300）：uid 拉取/quanto lazy 缓存+失效/签名 GET。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"nox/internal/platform/signutil"
)

// InvalidateQuanto D-570 包C S6③：contract_info 推送→本合约 lazy 缓存删除
// （exec.GateSpecInvalidate 镜像——collect 侧实例表同属永生病灶）。
func (g *GatePrivateWS) InvalidateQuanto(contract string) {
	g.quantoMu.Lock()
	delete(g.quanto, contract)
	g.quantoMu.Unlock()
}

// fetchUID 取 futures 账户 user id（签名 GET /futures/usdt/accounts，exec.GateSign 共享）。
func (g *GatePrivateWS) fetchUID(ctx context.Context) (string, error) {
	const endpoint = "/futures/usdt/accounts"
	ts := g.now().Unix()
	body, err := g.get(ctx, endpoint, ts)
	if err != nil {
		return "", err
	}
	var acc struct {
		User json.Number `json:"user"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return "", fmt.Errorf("accounts json: %w", err)
	}
	if acc.User.String() == "" {
		return "", fmt.Errorf("accounts response missing user field")
	}
	return acc.User.String(), nil
}

// gateQuanto contract→quanto_multiplier lazy 缓存（executor 同款；公开端点签名无害）。
func (g *GatePrivateWS) gateQuanto(ctx context.Context, contract string) (float64, error) {
	g.quantoMu.Lock()
	if q, ok := g.quanto[contract]; ok {
		g.quantoMu.Unlock()
		return q, nil
	}
	g.quantoMu.Unlock()
	body, err := g.get(ctx, "/futures/usdt/contracts/"+contract, g.now().Unix())
	if err != nil {
		return 0, err
	}
	var spec struct {
		QuantoMultiplier string `json:"quanto_multiplier"`
	}
	if err := json.Unmarshal(body, &spec); err != nil {
		return 0, fmt.Errorf("contract spec json: %w", err)
	}
	q, err := strconv.ParseFloat(spec.QuantoMultiplier, 64)
	if err != nil || q <= 0 {
		return 0, fmt.Errorf("contract %s quanto_multiplier=%q invalid", contract, spec.QuantoMultiplier)
	}
	g.quantoMu.Lock()
	g.quanto[contract] = q
	g.quantoMu.Unlock()
	return q, nil
}

// get 签名 GET（exec.GateSign 与 REST 路径同口径：urlPath=/api/v4+endpoint）。
func (g *GatePrivateWS) get(ctx context.Context, endpoint string, ts int64) ([]byte, error) {
	urlPath := "/api/v4" + endpoint
	sign := signutil.GateSign(g.apiSecret, "GET", urlPath, "", "", ts)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.baseURL+endpoint, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("KEY", g.apiKey)
	req.Header.Set("Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("SIGN", sign)
	resp, err := g.rest.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncateStr(string(data), 200))
	}
	return data, nil
}
