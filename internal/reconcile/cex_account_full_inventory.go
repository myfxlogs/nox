// cex_account_full_inventory.go —— D-365 S2：FullInventory 全币种盘点（仅财务页调用；
// monitor 5min 管道 Account() 保持 USDT-only 零杂币请求，两路径互不影响——分层不变量）。
// binance 计价链=D-364-R2 @14b1db33 实现原样移植（/v3/account 全量+ticker 逐币+LD 兜底序）；
// okx=/account/balance details 全 ccy eqUsd 直接取 + /asset/balances + /finance/savings/
// balance 子账户明细（路由核证点④见 cex_account_subaccounts.go：资产域直连 www.okx.com）。
package reconcile

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// FullInventory 全币种盘点结果（usdt_equity=USDT 标 1:1 和；holdings 全币种带价）。
type FullInventory struct {
	UsdtEquity   float64
	InventoryUSD float64 // Σ holdings usdt_value（全币种盘点总值，占比分母）
	Holdings     []*v1.Holding
}

// FullInventory 按 venue 分派全币种盘点。
func (q *CEXAccountQuerier) FullInventory(ctx context.Context) (*FullInventory, error) {
	switch q.venue {
	case "binance":
		return q.fullInventoryBinance(ctx)
	case "okx":
		return q.fullInventoryOkx(ctx)
	default:
		return nil, fmt.Errorf("full inventory: unsupported venue %q", q.venue)
	}
}

// binanceUsdtPrice 取资产 USDT 价（公开接口，无需签名）。D-365 v2.1 S2：随 FullInventory
// 恢复（D-364 v2 曾删——monitor 管道 USDT-only 不用；本函数仅财务页盘点明细计价使用）。
func (q *CEXAccountQuerier) binanceUsdtPrice(ctx context.Context, spotBase, asset string) (float64, error) {
	body, err := q.get(ctx, spotBase+"/v3/ticker/price?symbol="+asset+"USDT", nil) // 令牌由 httpx 注入
	if err != nil {
		return 0, err
	}
	var tp struct {
		Price string `json:"price"`
	}
	if err := json.Unmarshal(body, &tp); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(tp.Price, 64)
}

// fullInventoryBinance binance spot 全币种盘点：GET /v3/account 全量 + /v3/ticker/price
// 逐币计价 + LD 兜底序（D-364-R2 移植）。
func (q *CEXAccountQuerier) fullInventoryBinance(ctx context.Context) (*FullInventory, error) {
	spotBase := strings.Replace(q.baseURL, "fapi", "api", 1)
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	sig := binanceSign(q.apiSecret, params.Encode())
	params.Set("signature", sig)
	h := http.Header{}
	h.Set("X-MBX-APIKEY", q.apiKey)
	body, err := q.get(ctx, spotBase+"/v3/account?"+params.Encode(), h)
	if err != nil {
		return nil, fmt.Errorf("binance full inventory: %w", err)
	}
	var acc struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("binance full inventory json: %w", err)
	}
	inv := &FullInventory{}
	for _, b := range acc.Balances {
		free, _ := strconv.ParseFloat(b.Free, 64)
		locked, _ := strconv.ParseFloat(b.Locked, 64)
		qty := free + locked
		if qty <= 0 {
			continue
		}
		if b.Asset == "USDT" || strings.TrimPrefix(b.Asset, "LD") == "USDT" {
			// USDT 标：usdt_equity 计入（1:1），holdings 同行
			inv.UsdtEquity += qty
			inv.InventoryUSD += qty
			inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: b.Asset, Qty: qty, UsdtValue: qty})
			continue
		}
		// 杂币：LD 兜底序计价（D-364-R2 原样）——先原币防真币误剥，无对才剥前缀重查；
		// 仍无对 → 明细保留 usdt_value=0（不猜价）。
		price, perr := q.binanceUsdtPrice(ctx, spotBase, b.Asset)
		if perr != nil && strings.HasPrefix(b.Asset, "LD") {
			price, perr = q.binanceUsdtPrice(ctx, spotBase, strings.TrimPrefix(b.Asset, "LD"))
		}
		if perr != nil {
			inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: b.Asset, Qty: qty})
			continue
		}
		inv.InventoryUSD += qty * price
		inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: b.Asset, Qty: qty, UsdtValue: qty * price})
	}
	return inv, nil
}

// fullInventoryOkx okx 全币种盘点：交易账户 /account/balance details 全 ccy（eqUsd 直接取）
// + 资金账户 /asset/balances + 理财 /finance/savings/balance（USDT）三源合并。
func (q *CEXAccountQuerier) fullInventoryOkx(ctx context.Context) (*FullInventory, error) {
	const path = "/api/v5/account/balance"
	ts := time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	sign := okxSign(q.apiSecret, ts+"GET"+path)
	headers := http.Header{
		"OK-ACCESS-KEY":        {q.apiKey},
		"OK-ACCESS-SIGN":       {sign},
		"OK-ACCESS-TIMESTAMP":  {ts},
		"OK-ACCESS-PASSPHRASE": {q.passphrase},
		"Content-Type":         {"application/json"},
	}
	body, err := q.get(ctx, q.baseURL+path, headers)
	if err != nil {
		return nil, fmt.Errorf("okx full inventory: %w", err)
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			Details []struct {
				Ccy   string `json:"ccy"`
				EqUsd string `json:"eqUsd"`
			} `json:"details"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx full inventory json: %w", err)
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return nil, fmt.Errorf("okx full inventory: code=%s", resp.Code)
	}
	inv := &FullInventory{}
	for _, d := range resp.Data[0].Details {
		usd, perr := strconv.ParseFloat(d.EqUsd, 64)
		if perr != nil || usd == 0 {
			continue
		}
		if d.Ccy == "USDT" {
			inv.UsdtEquity += usd
		}
		inv.InventoryUSD += usd
		inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: d.Ccy, Qty: usd, UsdtValue: usd})
	}
	// 子账户两源合并（资金+理财，USDT-only——D-363 端点直连核证点④）：
	for _, src := range []struct {
		path  string
		field string
	}{{"/api/v5/asset/balances?ccy=USDT", "bal"}, {"/api/v5/finance/savings/balance?ccy=USDT", "amt"}} {
		sub, err := q.okxSubaccountSignedGet(ctx, src.path)
		if err != nil {
			return nil, fmt.Errorf("okx full inventory sub %s: %w", src.path, err)
		}
		usdt, rows, err := parseOkxUSDTBalance(sub, src.field)
		if err != nil {
			return nil, fmt.Errorf("okx full inventory sub %s parse: %w", src.path, err)
		}
		inv.UsdtEquity += usdt
		inv.InventoryUSD += usdt
		inv.Holdings = append(inv.Holdings, rows...)
	}
	return inv, nil
}
