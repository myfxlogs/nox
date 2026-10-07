// cex_account_full_inventory_sub.go —— D-365-R1：子账户/合约钱包盘点实现（自
// cex_account_full_inventory.go 拆出保 check-lines <300；分派语义见主文件）。
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

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/httpx"
)

// fullInventoryBinanceFunding binance 资金钱包盘点：POST /sapi/v1/asset/get-funding-asset
// 全量资产（DOGE 52,883 在此——核证点④）+ ticker 逐币计价（LD 剥前缀兜底同链）；
// USDT 行计 usdt_equity，其余仅进 holdings/inventory。
func (q *CEXAccountQuerier) fullInventoryBinanceFunding(ctx context.Context) (*FullInventory, error) {
	sapiBase := httpx.BinanceSapiBase(q.baseURL) // 核证点④：SAPI 走代理根路径
	tickerBase := sapiBase + "/api"              // D-365-R2 S1：ticker 是 spot 域端点，走 /api 前缀（根路径 /v3/ticker 404 实探）
	params := url.Values{}
	params.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	params.Set("recvWindow", "5000")
	sig := binanceSign(q.apiSecret, params.Encode())
	params.Set("signature", sig)
	h := http.Header{}
	h.Set("X-MBX-APIKEY", q.apiKey)
	body, err := q.post(ctx, sapiBase+"/sapi/v1/asset/get-funding-asset?"+params.Encode(), h)
	if err != nil {
		return nil, fmt.Errorf("binance funding full inventory: %w", err)
	}
	var rows []struct {
		Asset  string `json:"asset"`
		Free   string `json:"free"`
		Locked string `json:"locked"`
	}
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("binance funding full inventory json: %w", err)
	}
	inv := &FullInventory{}
	for _, r := range rows {
		free, perr := f64z(r.Free)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "r.Free", r.Free, perr)
		}
		locked, perr := f64z(r.Locked)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "r.Locked", r.Locked, perr)
		}
		qty := free + locked
		if qty <= 0 {
			continue
		}
		if r.Asset == "USDT" {
			inv.UsdtEquity += qty
			inv.InventoryUSD += qty
			inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: r.Asset, Qty: qty, UsdtValue: qty})
			continue
		}
		asset := strings.TrimPrefix(r.Asset, "LD") // LD 封装兜底同链（资金钱包现无 LD，防御一致）
		price, perr := q.binanceUsdtPrice(ctx, tickerBase, asset)
		if perr != nil {
			inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: r.Asset, Qty: qty})
			continue
		}
		inv.InventoryUSD += qty * price
		inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: r.Asset, Qty: qty, UsdtValue: qty * price})
	}
	return inv, nil
}

// fullInventoryOkxBySource okx 子账户盘点（funding=/asset/balances 全 ccy；
// earn=/finance/savings/balance 全 ccy——R1 去 ?ccy=USDT 限制）。非 USDT 计 qty +
// index-tickers（CCY-USDT）计价；无对 → 明细保留不猜价。
func (q *CEXAccountQuerier) fullInventoryOkxBySource(ctx context.Context, source string) (*FullInventory, error) {
	var path, field string
	switch source {
	case "funding":
		path, field = "/api/v5/asset/balances", "bal"
	case "earn":
		path, field = "/api/v5/finance/savings/balance", "amt"
	default:
		return nil, fmt.Errorf("bad okx source %q", source)
	}
	body, err := q.okxSubaccountSignedGet(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("okx %s full inventory: %w", source, err)
	}
	var resp struct {
		Code string              `json:"code"`
		Msg  string              `json:"msg"`
		Data []map[string]string `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("okx %s full inventory json: %w", source, err)
	}
	if resp.Code != "0" {
		return nil, fmt.Errorf("okx %s: code=%s msg=%s", source, resp.Code, resp.Msg)
	}
	inv := &FullInventory{}
	for _, d := range resp.Data {
		ccy := d["ccy"]
		v, perr := strconv.ParseFloat(d[field], 64)
		if perr != nil || v <= 0 {
			continue
		}
		if ccy == "USDT" {
			inv.UsdtEquity += v
			inv.InventoryUSD += v
			inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: ccy, Qty: v, UsdtValue: v})
			continue
		}
		usd, perr := q.okxIndexPrice(ctx, ccy)
		if perr != nil {
			inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: ccy, Qty: v})
			continue
		}
		inv.InventoryUSD += v * usd
		inv.Holdings = append(inv.Holdings, &v1.Holding{Ccy: ccy, Qty: v, UsdtValue: v * usd})
	}
	return inv, nil
}

// okxIndexPrice okx 指数价（/market/index-tickers?instId=CCY-USDT；直连核证点④）。
func (q *CEXAccountQuerier) okxIndexPrice(ctx context.Context, ccy string) (float64, error) {
	body, err := q.okxSubaccountSignedGet(ctx, "/api/v5/market/index-tickers?instId="+ccy+"-USDT")
	if err != nil {
		return 0, err
	}
	var resp struct {
		Code string `json:"code"`
		Data []struct {
			IdxPx string `json:"idxPx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		return 0, err
	}
	if resp.Code != "0" || len(resp.Data) == 0 {
		return 0, fmt.Errorf("no index for %s", ccy)
	}
	return strconv.ParseFloat(resp.Data[0].IdxPx, 64)
}
