package exec

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SetMarginReservePct 注入资金预检安全垫（cfg.MarginReservePct；不设=缺省 0.20）。
func (e *CEXExecutor) SetMarginReservePct(pct float64) { e.marginReservePct = pct }

// MarginPrecheck D-415 §1.3：OKX perp venue 原生可开量校验——
// GET /api/v5/account/max-avail-size?instId&tdMode=cross（服务端已含余额+杠杆+持仓，
// 无估算）返回 maxBuy/maxSell（张数）；szContracts ≤ avail×(1−reservePct) 才放行。
// 范围钉死 §1.6：只实现 okx:perp——其余 venue/type 返 nil（等效未实现跳过，走拒单兜底）。
// 任一调用失败 → 返 err 拒开（fail-closed）。
func (e *CEXExecutor) MarginPrecheck(ctx context.Context, o Order) error {
	if e.venue != "okx" || e.venueType != "perp" {
		return nil
	}
	instID := o.Symbol
	if !strings.HasSuffix(instID, "-SWAP") {
		instID += "-SWAP"
	}
	ctVal, _, err := e.okxContractVal(ctx, instID)
	if err != nil {
		return fmt.Errorf("okx margin precheck %s: %w", instID, err)
	}
	szContracts := o.Qty / ctVal

	path := "/api/v5/account/max-avail-size?instId=" + url.QueryEscape(instID) + "&tdMode=cross"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}
	body, err := e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return fmt.Errorf("okx max-avail-size %s: %w", instID, err)
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			MaxBuy  string `json:"maxBuy"`
			MaxSell string `json:"maxSell"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("okx max-avail-size %s json: %w", instID, err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return fmt.Errorf("okx max-avail-size %s: code=%s data_len=%d", instID, resp.Code, len(resp.Data))
	}
	availStr := resp.Data[0].MaxBuy
	if o.Side == "SELL" {
		availStr = resp.Data[0].MaxSell
	}
	avail, err := strconv.ParseFloat(availStr, 64)
	if err != nil || avail < 0 {
		return fmt.Errorf("okx max-avail-size %s: avail=%q invalid", instID, availStr)
	}
	if szContracts > avail*(1-e.marginReservePct) {
		return fmt.Errorf("okx margin insufficient: sz %.4f 张 > avail %.4f 张 × %.2f (%s)",
			szContracts, avail, 1-e.marginReservePct, instID)
	}
	return nil
}
