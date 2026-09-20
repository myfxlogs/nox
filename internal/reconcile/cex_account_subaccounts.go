// cex_account_subaccounts.go —— D-363 S2：CEX 资金（funding）/理财（earn）子账户查询。
// 只报 USDT 余额（D-364 v2 同语义：非 USDT 标不读取不统计，DOGE/USDC 等全跳）。
// 路由核证点④（9-20 真 key 实探留证）：binance sapi 走代理根路径（/api 前缀 404 实探）；
// okx 资金/理财端点代理不路由（403 实探）→ 直连 www.okx.com；交易账户 /account/balance
// 代理路由不受影响（生产在跑）。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// post 发起 POST 并读回 body（非 200 → 错误带响应体；binance sapi 类 POST+query 签名端点用）。
func (q *CEXAccountQuerier) post(ctx context.Context, u string, headers http.Header) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := q.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	return data, nil
}

// Account 子账户分派（D-363 S2）：venue_type=funding/earn 先于交易账户 venue 分派。
// gate 无 funding/earn 配置（仅 sim，种子不种）→ default 报错同 gate spot 先例。
func (q *CEXAccountQuerier) accountSubaccount(ctx context.Context) (*AccountInfo, error) {
	switch {
	case q.venueType == "funding":
		switch q.venue {
		case "binance":
			return q.accountBinanceFunding(ctx)
		case "okx":
			return q.accountOkxFunding(ctx)
		}
	case q.venueType == "earn":
		if q.venue == "okx" {
			return q.accountOkxEarn(ctx)
		}
		// binance earn：核证点②（9-20 实探 locked/position → {"total":0,"rows":[]}，
		// 零 locked 仓）→ 按设计「无则行缺省不列」省略；活期 LDUSDT 已由 spot 行覆盖。
		return nil, fmt.Errorf("earn account: unsupported venue %q（locked 零仓省略）", q.venue)
	}
	return nil, fmt.Errorf("subaccount: unsupported %s:%s", q.venue, q.venueType)
}

// accountBinanceFunding 资金钱包：POST /sapi/v1/asset/get-funding-asset（HMAC 走代理根路径）
// → 只取 asset=="USDT" 行 free+locked（USDT-only，其余 asset 不读取）。
func (q *CEXAccountQuerier) accountBinanceFunding(ctx context.Context) (*AccountInfo, error) {
	sapiBase := strings.TrimSuffix(q.baseURL, "/api") // 核证点④：sapi 走代理根路径
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	sig := binanceSign(q.apiSecret, params.Encode())
	params.Set("signature", sig)
	h := http.Header{}
	h.Set("X-MBX-APIKEY", q.apiKey)
	body, err := q.post(ctx, sapiBase+"/sapi/v1/asset/get-funding-asset?"+params.Encode(), h)
	if err != nil {
		return nil, fmt.Errorf("binance funding: %w", err)
	}
	var rows []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("binance funding json: %w", err)
	}
	eq := 0.0
	var holdings []*v1.Holding
	for _, r := range rows {
		if r.Asset != "USDT" {
			continue // USDT-only：DOGE/USDC 等不读取不统计
		}
		free, _ := strconv.ParseFloat(r.Free, 64)
		locked, _ := strconv.ParseFloat(r.Locked, 64)
		eq = free + locked
		holdings = append(holdings, &v1.Holding{Ccy: "USDT", Qty: eq, UsdtValue: eq})
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}

// okxSubaccountBase okx 资金/理财端点直连基址（核证点④：okx 资金端点代理不路由 403 实探
// → 直连 www.okx.com；config base=代理 IP 对交易账户 /account/balance 有效但不适用本端点。
// 包级变量=测试 httptest 注入点）。种子 config 的 base_url（复制自 spot 代理行）对本端点
// 刻意忽略。
var okxSubaccountBase = "https://www.okx.com"

// okxSubaccountSignedGet okx 子账户端点签名 GET（签名同 accountOKX：prehash=ts+method+path+query）。
func (q *CEXAccountQuerier) okxSubaccountSignedGet(ctx context.Context, pathWithQuery string) ([]byte, error) {
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sign := okxSign(q.apiSecret, ts+"GET"+pathWithQuery)
	headers := http.Header{
		"OK-ACCESS-KEY":        {q.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {q.passphrase},
		"Content-Type":         {"application/json"},
	}
	return q.get(ctx, okxSubaccountBase+pathWithQuery, headers)
}

// accountOkxFunding okx 资金账户：GET /api/v5/asset/balances?ccy=USDT → USDT 行 bal（真探
// 9-20：USDT 0=资金账户空）。
func (q *CEXAccountQuerier) accountOkxFunding(ctx context.Context) (*AccountInfo, error) {
	body, err := q.okxSubaccountSignedGet(ctx, "/api/v5/asset/balances?ccy=USDT")
	if err != nil {
		return nil, fmt.Errorf("okx funding: %w", err)
	}
	eq, holdings, err := parseOkxUSDTBalance(body, "bal")
	if err != nil {
		return nil, fmt.Errorf("okx funding json: %w", err)
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}

// accountOkxEarn okx 理财（活期储蓄）：GET /api/v5/finance/savings/balance?ccy=USDT →
// USDT 行 amt（真探 9-20：amt=5261.25/rate=0.01/earnings=$69.64 在档）→ equity=amt。
func (q *CEXAccountQuerier) accountOkxEarn(ctx context.Context) (*AccountInfo, error) {
	body, err := q.okxSubaccountSignedGet(ctx, "/api/v5/finance/savings/balance?ccy=USDT")
	if err != nil {
		return nil, fmt.Errorf("okx earn: %w", err)
	}
	eq, holdings, err := parseOkxUSDTBalance(body, "amt")
	if err != nil {
		return nil, fmt.Errorf("okx earn json: %w", err)
	}
	return &AccountInfo{
		VenueKey: q.VenueKey(), Venue: q.venue, VenueType: q.venueType, AssetType: "cex",
		Balance: eq, Equity: eq, Currency: "USDT",
		Simulated: q.simulated, Website: q.website, Holdings: holdings,
	}, nil
}

// parseOkxUSDTBalance okx 子账户响应（/asset/balances 与 /finance/savings/balance 同形：
// data[] 行含 ccy+数量字段）——只取 USDT 行数量字段 field 为 equity，holdings 仅 USDT 行。
func parseOkxUSDTBalance(body []byte, field string) (eq float64, holdings []*v1.Holding, err error) {
	var resp struct {
		Code string              `json:"code"`
		Msg  string              `json:"msg"`
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, nil, err
	}
	if resp.Code != "0" {
		return 0, nil, fmt.Errorf("code=%s msg=%s", resp.Code, resp.Msg)
	}
	for _, d := range resp.Data {
		if d["ccy"] != "USDT" {
			continue
		}
		v, perr := strconv.ParseFloat(d[field], 64)
		if perr != nil {
			continue
		}
		eq = v
		if v > 0 {
			holdings = append(holdings, &v1.Holding{Ccy: "USDT", Qty: v, UsdtValue: v})
		}
		break
	}
	return eq, holdings, nil
}
