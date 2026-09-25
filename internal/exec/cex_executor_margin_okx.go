// cex_executor_margin_okx.go —— D-479 S1：自 cex_executor_margin.go 按 venue 缝整段搬移（纯搬移零逻辑变更）。
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

func (e *CEXExecutor) okxMarginPrecheck(ctx context.Context, o Order) error {
	instID := o.Symbol
	if !strings.HasSuffix(instID, "-SWAP") {
		instID += "-SWAP"
	}
	ctVal, _, err := e.okxContractVal(ctx, instID)
	if err != nil {
		return fmt.Errorf("okx margin precheck %s: %w", instID, err)
	}
	szContracts := o.Qty / ctVal

	path := "/api/v5/account/max-size?instId=" + url.QueryEscape(instID) + "&tdMode=cross"
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
		return fmt.Errorf("okx max-size %s: %w", instID, err)
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			MaxBuy  string `json:"maxBuy"`
			MaxSell string `json:"maxSell"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return fmt.Errorf("okx max-size %s json: %w", instID, err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return fmt.Errorf("okx max-size %s: code=%s data_len=%d", instID, resp.Code, len(resp.Data))
	}
	availStr := resp.Data[0].MaxBuy
	if o.Side == "SELL" {
		availStr = resp.Data[0].MaxSell
	}
	avail, err := strconv.ParseFloat(availStr, 64)
	if err != nil || avail < 0 {
		return fmt.Errorf("okx max-size %s: avail=%q invalid", instID, availStr)
	}
	if szContracts > avail*(1-e.marginReservePct) {
		return fmt.Errorf("okx margin insufficient: sz %.4f 张 > avail %.4f 张 × %.2f (%s)",
			szContracts, avail, 1-e.marginReservePct, instID)
	}
	return nil
}

// okxMarkAndEquity D-472 S4：okx 标记价（公开 mark-price）+ 账户权益（signed balance totalEq）。
func (e *CEXExecutor) okxMarkAndEquity(ctx context.Context, symbol string) (mark, equity float64, err error) {
	instID := symbol
	if !strings.HasSuffix(instID, "-SWAP") {
		instID += "-SWAP"
	}
	body, err := e.get(ctx, e.baseURL+"/api/v5/public/mark-price?instId="+url.QueryEscape(instID), nil)
	if err != nil {
		return 0, 0, fmt.Errorf("mark-price: %w", err)
	}
	var mp struct {
		Data []struct {
			MarkPx string `json:"markPx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &mp); err != nil || len(mp.Data) == 0 {
		return 0, 0, fmt.Errorf("mark-price %s: len=%d err=%v", instID, len(mp.Data), err)
	}
	mark, err = strconv.ParseFloat(mp.Data[0].MarkPx, 64)
	if err != nil || mark <= 0 {
		return 0, 0, fmt.Errorf("mark-price %s: markPx=%q invalid", instID, mp.Data[0].MarkPx)
	}

	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	path := "/api/v5/account/balance"
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	if e.simulated {
		headers.Set("x-simulated-trading", "1")
	}
	body, err = e.get(ctx, e.baseURL+path, headers)
	if err != nil {
		return 0, 0, fmt.Errorf("balance: %w", err)
	}
	var bal struct {
		Code string `json:"code"`
		Data []struct {
			TotalEq string `json:"totalEq"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &bal); err != nil {
		return 0, 0, fmt.Errorf("balance json: %w", err)
	}
	if bal.Code != "0" || len(bal.Data) == 0 {
		return 0, 0, fmt.Errorf("balance: code=%s data_len=%d", bal.Code, len(bal.Data))
	}
	equity, err = strconv.ParseFloat(bal.Data[0].TotalEq, 64)
	if err != nil || equity < 0 {
		return 0, 0, fmt.Errorf("balance: totalEq=%q invalid", bal.Data[0].TotalEq)
	}
	return mark, equity, nil
}

// binanceMarginPrecheck D-472 S1：binance perp 资金预检（原空实现返 nil 补实）。
// signed GET /v2/account（baseURL 已含 /fapi 段，同 placeBinance /v1/order 惯例）→ availableBalance + positions[]（per-symbol leverage/
// isolated/positionSide）；mark 用公开 /fapi/v1/premiumIndex（baseURL 免签名路径）。
// requiredIM = o.Qty(base) × mark / leverage（B-001 禁二次乘 ct_val——binance ct_val≡1）。
