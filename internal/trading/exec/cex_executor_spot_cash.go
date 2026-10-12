package exec

// cex_executor_spot_cash.go D-473 S2：spot 现金预检——carry 场景现货腿全额现金占用，
// 原 MarginPrecheck spot 分支返 nil 未核。只对 BUY 腿判定（SELL 释放现金不查）；
// 余额/mark 端点任一取不到 → error 拒开（fail-closed，对齐 D-472 语义）。
//
// required = qty(基础币) × mark × (1 + takerBps/1e4)，对报价币可用余额。
// 报价币解析 per venue 符号惯例（okx BASE-USDT / gate BASE_USDT / binance BASEUSDT
// 后缀集）；解析不出 → error 不猜。
//
// 字段名按实测响应取（api-response-fields-empirical 条目）：
//   - binance GET base+/v3/account（HMAC 签名）→ balances[]{asset,free}
//   - okx     GET /api/v5/account/balance?ccy=Q（四头签名）→ details[]{ccy,availBal}
//   - gate    GET /api/v4/spot/accounts?currency=Q（KEY/SIGN）→ []{available}
// mark=公开 ticker：okx /api/v5/market/ticker?instId → last；
// binance base+/v3/ticker/price → price；gate /api/v4/spot/tickers?currency_pair → last。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"nox/internal/platform/signutil"
	"strconv"
	"strings"
)

// SetSpotTakerBps D-473 S2：注入 spot 腿 taker 费率（bps，cfg.CommissionTaker[venue]
// 回退 CommissionCEXTakerBps），现金占用预估缓冲用。
func (e *CEXExecutor) SetSpotTakerBps(bps float64) { e.spotTakerBps = bps }

// spotCashPrecheck spot BUY 腿现金预检（SELL 腿不查直接放行）。
func (e *CEXExecutor) spotCashPrecheck(ctx context.Context, o Order) error {
	if o.Side != "BUY" {
		return nil
	}
	quote, err := spotQuoteCcy(e.venue, o.Symbol)
	if err != nil {
		return fmt.Errorf("%s spot cash precheck %s: %w", e.venue, o.Symbol, err)
	}
	mark, err := e.spotMarkPrice(ctx, o.Symbol)
	if err != nil {
		return fmt.Errorf("%s spot cash precheck %s: %w", e.venue, o.Symbol, err)
	}
	avail, err := e.spotQuoteAvail(ctx, quote)
	if err != nil {
		return fmt.Errorf("%s spot cash precheck %s: %w", e.venue, o.Symbol, err)
	}
	required := o.Qty * mark * (1 + e.spotTakerBps/1e4)
	if required > avail*(1-e.marginReservePct) {
		return fmt.Errorf("%s spot cash insufficient: need %.2f %s > avail %.2f × %.2f (qty %.6f mark %.4g taker %.1fbps)",
			e.venue, required, quote, avail, 1-e.marginReservePct, o.Qty, mark, e.spotTakerBps)
	}
	return nil
}

// spotQuoteCcy 解析现货符号报价币（不猜——格式不符返 error fail-closed）。
func spotQuoteCcy(venue, symbol string) (string, error) {
	switch venue {
	case "okx":
		if i := strings.LastIndex(symbol, "-"); i > 0 && i < len(symbol)-1 {
			return symbol[i+1:], nil
		}
	case "gate":
		if i := strings.LastIndex(symbol, "_"); i > 0 && i < len(symbol)-1 {
			return symbol[i+1:], nil
		}
	case "binance":
		for _, q := range []string{"USDT", "USDC", "FDUSD"} {
			if strings.HasSuffix(symbol, q) && len(symbol) > len(q) {
				return q, nil
			}
		}
	}
	return "", fmt.Errorf("报价币解析失败（venue=%s symbol=%s 不识格式）", venue, symbol)
}

// spotMarkPrice 现货腿现价（公开 ticker，取不到→err fail-closed）。
func (e *CEXExecutor) spotMarkPrice(ctx context.Context, symbol string) (float64, error) {
	switch e.venue {
	case "okx":
		body, err := e.get(ctx, e.baseURL+"/api/v5/market/ticker?instId="+url.QueryEscape(symbol), nil)
		if err != nil {
			return 0, fmt.Errorf("ticker: %w", err)
		}
		var tk struct {
			Code string `json:"code"`
			Data []struct {
				Last string `json:"last"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &tk); err != nil {
			return 0, fmt.Errorf("ticker json: %w", err)
		}
		if tk.Code != "0" || len(tk.Data) == 0 {
			return 0, fmt.Errorf("ticker %s: code=%s data_len=%d", symbol, tk.Code, len(tk.Data))
		}
		return parsePositive(tk.Data[0].Last, "last")
	case "binance":
		body, err := e.get(ctx, e.baseURL+"/v3/ticker/price?symbol="+url.QueryEscape(symbol), nil)
		if err != nil {
			return 0, fmt.Errorf("ticker: %w", err)
		}
		var tk struct {
			Price string `json:"price"`
		}
		if err := json.Unmarshal(body, &tk); err != nil {
			return 0, fmt.Errorf("ticker json: %w", err)
		}
		return parsePositive(tk.Price, "price")
	case "gate":
		endpoint := "/spot/tickers?currency_pair=" + url.QueryEscape(symbol)
		body, err := e.get(ctx, e.baseURL+endpoint, nil)
		if err != nil {
			return 0, fmt.Errorf("tickers: %w", err)
		}
		var rows []struct {
			Last string `json:"last"`
		}
		if err := json.Unmarshal(body, &rows); err != nil {
			return 0, fmt.Errorf("tickers json: %w", err)
		}
		if len(rows) == 0 {
			return 0, fmt.Errorf("tickers %s: empty", symbol)
		}
		return parsePositive(rows[0].Last, "last")
	}
	return 0, fmt.Errorf("venue %q spot mark 端点未实现", e.venue)
}

// spotQuoteAvail 报价币可用余额（签名端点，取不到→err fail-closed）。
func (e *CEXExecutor) spotQuoteAvail(ctx context.Context, quote string) (float64, error) {
	switch e.venue {
	case "okx":
		path := "/api/v5/account/balance?ccy=" + url.QueryEscape(quote)
		ts := e.okxNowISO()
		headers := http.Header{
			"OK-ACCESS-KEY":        {e.apiKey},
			"OK-ACCESS-SIGN":       {okxSign(e.apiSecret, ts+"GET"+path)},
			"OK-ACCESS-TIMESTAMP":  {ts},
			"OK-ACCESS-PASSPHRASE": {e.passphrase},
		}
		body, err := e.get(ctx, e.baseURL+path, headers)
		if err != nil {
			return 0, fmt.Errorf("balance: %w", err)
		}
		var resp struct {
			Code string `json:"code"`
			Data []struct {
				Details []struct {
					Ccy      string `json:"ccy"`
					AvailBal string `json:"availBal"`
				} `json:"details"`
			} `json:"data"`
		}
		if err := json.Unmarshal(body, &resp); err != nil {
			return 0, fmt.Errorf("balance json: %w", err)
		}
		if resp.Code != "0" || len(resp.Data) == 0 {
			return 0, fmt.Errorf("balance: code=%s data_len=%d", resp.Code, len(resp.Data))
		}
		for _, d := range resp.Data[0].Details {
			if d.Ccy == quote {
				return parseNonNegative(d.AvailBal, "availBal")
			}
		}
		return 0, fmt.Errorf("balance: %s 行缺（报价币不可判不猜）", quote)
	case "binance":
		acc, err := e.signedBinanceRequest(ctx, "GET", "/v3/account", url.Values{})
		if err != nil {
			return 0, fmt.Errorf("account: %w", err)
		}
		balances, _ := acc["balances"].([]any)
		for _, b := range balances {
			bm, ok := b.(map[string]any)
			if !ok || bm["asset"] != quote {
				continue
			}
			free, _ := bm["free"].(string)
			return parseNonNegative(free, "free")
		}
		return 0, fmt.Errorf("account: %s 行缺（报价币不可判不猜）", quote)
	case "gate":
		endpoint := "/spot/accounts?currency=" + url.QueryEscape(quote)
		urlPath := "/api/v4" + endpoint
		ts := gateNow()
		headers := http.Header{
			"KEY":       {e.apiKey},
			"Timestamp": {strconv.FormatInt(ts, 10)},
			"SIGN":      {signutil.GateSign(e.apiSecret, "GET", urlPath, "currency="+url.QueryEscape(quote), "", ts)},
		}
		body, err := e.get(ctx, e.baseURL+endpoint, headers)
		if err != nil {
			return 0, fmt.Errorf("spot accounts: %w", err)
		}
		var rows []struct {
			Currency  string `json:"currency"`
			Available string `json:"available"`
		}
		if err := json.Unmarshal(body, &rows); err != nil {
			return 0, fmt.Errorf("spot accounts json: %w", err)
		}
		for _, r := range rows {
			if r.Currency == quote {
				return parseNonNegative(r.Available, "available")
			}
		}
		return 0, fmt.Errorf("spot accounts: %s 行缺（报价币不可判不猜）", quote)
	}
	return 0, fmt.Errorf("venue %q spot 余额端点未实现", e.venue)
}

func parsePositive(s, field string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v <= 0 {
		return 0, fmt.Errorf("%s=%q invalid", field, s)
	}
	return v, nil
}

func parseNonNegative(s, field string) (float64, error) {
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || v < 0 {
		return 0, fmt.Errorf("%s=%q invalid", field, s)
	}
	return v, nil
}
