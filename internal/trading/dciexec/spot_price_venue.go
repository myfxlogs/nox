// spot_price_venue.go —— 现货参考价 venue 分派面（bnapi.go 行数棘轮拆分）。
// 拿错所价=strike 界失真（alert#3373：okx 提案撞 binance 空 base 裸错病灶）。
package dciexec

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// errSpotNoBase 现货价读径 base URL/凭据行缺哨兵——venue 无 APIBase/EarnCreds 行
// 时返回，调用方按 credential 缺口落请求箱（禁裸 scheme/网络错冒 runtime）。
var errSpotNoBase = errors.New("现货价读径 venue base/凭据行缺")

// spotPriceVenue 现货参考价按 venue 分派（交割锚同径 venue 价源——拿错所价
// =strike 界失真）：
//   - binance→{APIBase}/v3/ticker/price price
//   - okx→{base}/api/v5/market/index-tickers?instId={BASE}-USDT idxPx
//     （cex_executor/reconcile 同径先例；指数价=交割结算锚 venue 原生口径）
//   - gate→{base}/api/v4/spot/tickers?currency_pair={BASE}_USDT rows[0].last
//
// symbol 统一收 binance 形（BTCUSDT）；内部剥 USDT 尾得基币拼 venue 原生符号。
// venue 凭据行/BaseURL 缺 → errSpotNoBase（fail-closed，不发无 host 裸请求）。
func (e *Executor) spotPriceVenue(ctx context.Context, venue, symbol string) (float64, error) {
	if e.PriceFn != nil {
		return e.PriceFn(ctx, symbol)
	}
	base := strings.TrimSuffix(strings.ToUpper(symbol), "USDT")
	switch venue {
	case "okx":
		c, ok := e.EarnCreds["okx"]
		if !ok || strings.TrimSpace(c.BaseURL) == "" {
			return 0, fmt.Errorf("%w: okx", errSpotNoBase)
		}
		return e.okxIndexPrice(ctx, strings.TrimRight(c.BaseURL, "/"), base)
	case "gate":
		c, ok := e.EarnCreds["gate"]
		if !ok || strings.TrimSpace(c.BaseURL) == "" {
			return 0, fmt.Errorf("%w: gate", errSpotNoBase)
		}
		return e.gateSpotPrice(ctx, gateAPIBase(c.BaseURL), base)
	default: // binance 及未知 venue 回落原径（未知 venue 报读败=fail-closed 同形）
		if strings.TrimSpace(e.APIBase) == "" {
			return 0, fmt.Errorf("%w: %s", errSpotNoBase, venue)
		}
		url := fmt.Sprintf("%s/v3/ticker/price?symbol=%s",
			strings.TrimRight(e.APIBase, "/"), symbol)
		b, err := e.get(ctx, url, false)
		if err != nil {
			return 0, err
		}
		var r struct {
			Price string `json:"price"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			return 0, err
		}
		return strconv.ParseFloat(r.Price, 64)
	}
}

// okxIndexPrice okx 指数价（/api/v5/market/index-tickers?instId={base}-USDT，
// data[0].idxPx——reconcile.okxIndexPrice 同径；公开端点零签名）。
func (e *Executor) okxIndexPrice(ctx context.Context, apiBase, baseCcy string) (float64, error) {
	url := fmt.Sprintf("%s/api/v5/market/index-tickers?instId=%s-USDT", apiBase, baseCcy)
	b, err := e.get(ctx, url, false)
	if err != nil {
		return 0, err
	}
	var r struct {
		Code string `json:"code"`
		Data []struct {
			IdxPx string `json:"idxPx"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return 0, err
	}
	if r.Code != "0" || len(r.Data) == 0 {
		return 0, fmt.Errorf("okx index-tickers %s-USDT: code=%s data_len=%d", baseCcy, r.Code, len(r.Data))
	}
	return strconv.ParseFloat(r.Data[0].IdxPx, 64)
}

// gateSpotPrice gate 现货价（/api/v4/spot/tickers?currency_pair={base}_USDT，
// 公开端点零签名；cex_executor_spot_cash.go 同径先例）。
func (e *Executor) gateSpotPrice(ctx context.Context, apiBase, baseCcy string) (float64, error) {
	url := fmt.Sprintf("%s/api/v4/spot/tickers?currency_pair=%s_USDT", apiBase, baseCcy)
	b, err := e.get(ctx, url, false)
	if err != nil {
		return 0, err
	}
	var rows []struct {
		Last string `json:"last"`
	}
	if err := json.Unmarshal(b, &rows); err != nil {
		return 0, err
	}
	if len(rows) == 0 {
		return 0, fmt.Errorf("gate tickers %s_USDT: empty", baseCcy)
	}
	return strconv.ParseFloat(rows[0].Last, 64)
}
