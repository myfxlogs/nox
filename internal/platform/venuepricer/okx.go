// okx.go —— okx 定价适配器（index-tickers idxPx 交割锚口径 + history-candles σ）。
package venuepricer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type okxAdapter struct{ c Cred }

func init() { Register("okx", func(c Cred) Adapter { return &okxAdapter{c} }) }

func (a *okxAdapter) SpotSymbol(base string) string { return strings.ToUpper(base) + "-USDT" }

// SpotPrice GET {base}/api/v5/market/index-tickers?instId={BASE}-USDT idxPx
// （reconcile.okxIndexPrice 同径；指数价=交割结算锚 venue 原生口径）。
func (a *okxAdapter) SpotPrice(ctx context.Context, base string) (float64, error) {
	u := fmt.Sprintf("%s/api/v5/market/index-tickers?instId=%s", strings.TrimRight(a.c.BaseURL, "/"), a.SpotSymbol(base))
	b, err := httpGet(ctx, a.c.Client, u)
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
		return 0, fmt.Errorf("okx index-tickers %s: code=%s data_len=%d", a.SpotSymbol(base), r.Code, len(r.Data))
	}
	return strconv.ParseFloat(r.Data[0].IdxPx, 64)
}

func (a *okxAdapter) SigmaDaily(ctx context.Context, base string, days int) (float64, error) {
	if a.c.SigmaFn == nil {
		return 0, fmt.Errorf("okx σ 引擎未注入（Cred.SigmaFn 缺）")
	}
	return a.c.SigmaFn(ctx, a.c.Client, a.c.BaseURL, "okx", a.SpotSymbol(base), days)
}
