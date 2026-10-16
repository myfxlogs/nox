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
)

func (e *CEXExecutor) okxMarginPrecheck(ctx context.Context, o Order) error {
	instID := o.Symbol
	if !strings.HasSuffix(instID, "-SWAP") {
		instID += "-SWAP"
	}
	ctVal, _, _, err := e.okxContractVal(ctx, instID)
	if err != nil {
		return fmt.Errorf("okx margin precheck %s: %w", instID, err)
	}
	szContracts := o.Qty / ctVal

	path := "/api/v5/account/max-size?instId=" + url.QueryEscape(instID) + "&tdMode=cross"
	ts := e.okxNowISO()
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
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
		// D-549 通用化：unified 池缺口 → 所内装配（savings 赎→funding→6→18 划转）
		// 后重读 max-size 复判；装配败=fail-closed 如实拒因（禁放大禁预腾）。
		mark, merr := e.okxMarkOnly(ctx, instID)
		if merr == nil {
			needUSD := (szContracts - avail*(1-e.marginReservePct)) * ctVal * mark
			if _, aerr := e.okxAssembleMargin(ctx, needUSD); aerr != nil {
				return fmt.Errorf("okx margin insufficient（装配败: %v）: sz %.4f > avail %.4f × %.2f (%s)",
					aerr, szContracts, avail, 1-e.marginReservePct, instID)
			}
			// 装配落位 → max-size 重读复判（venue 口径真值，不自算放行）
			if err := e.okxMaxSize(ctx, instID, o.Side, &avail); err != nil {
				return fmt.Errorf("okx margin 装配后重读 %s: %w", instID, err)
			}
			if szContracts > avail*(1-e.marginReservePct) {
				return fmt.Errorf("okx margin insufficient（装配后仍不足）: sz %.4f 张 > avail %.4f 张 × %.2f (%s)",
					szContracts, avail, 1-e.marginReservePct, instID)
			}
			return nil
		}
		return fmt.Errorf("okx margin insufficient: sz %.4f 张 > avail %.4f 张 × %.2f (%s)",
			szContracts, avail, 1-e.marginReservePct, instID)
	}
	return nil
}

// okxMaxSize max-size 重读（装配后复判用）——avail 指针回写。
func (e *CEXExecutor) okxMaxSize(ctx context.Context, instID, side string, avail *float64) error {
	path := "/api/v5/account/max-size?instId=" + url.QueryEscape(instID) + "&tdMode=cross"
	ts := e.okxNowISO()
	h := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
	}
	body, err := e.get(ctx, e.baseURL+path, h)
	if err != nil {
		return err
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			MaxBuy  string `json:"maxBuy"`
			MaxSell string `json:"maxSell"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return err
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return fmt.Errorf("code=%s len=%d", resp.Code, len(resp.Data))
	}
	a := resp.Data[0].MaxBuy
	if side == "SELL" {
		a = resp.Data[0].MaxSell
	}
	v, err := strconv.ParseFloat(a, 64)
	if err != nil || v < 0 {
		return fmt.Errorf("avail=%q invalid", a)
	}
	*avail = v
	return nil
}

// okxMarkOnly 标记价单读（装配缺口折算用——okxMarkAndEquity 的 mark 支）。
func (e *CEXExecutor) okxMarkOnly(ctx context.Context, instID string) (float64, error) {
	body, err := e.get(ctx, e.baseURL+"/api/v5/public/mark-price?instId="+url.QueryEscape(instID), nil)
	if err != nil {
		return 0, err
	}
	var mp struct {
		Data []struct {
			MarkPx string `json:"markPx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &mp); err != nil || len(mp.Data) == 0 {
		return 0, fmt.Errorf("mark-price len=%d err=%v", len(mp.Data), err)
	}
	return strconv.ParseFloat(mp.Data[0].MarkPx, 64)
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

	ts := e.okxNowISO()
	path := "/api/v5/account/balance"
	headers := http.Header{
		"OK-ACCESS-KEY":        {e.apiKey},
		"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {e.passphrase},
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
