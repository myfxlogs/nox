// binance.go —— binance 定价适配器（/v3/ticker/price + fapi K 线 σ）。
package venuepricer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type binanceAdapter struct{ c Cred }

func init() { Register("binance", func(c Cred) Adapter { return &binanceAdapter{c} }) }

func (a *binanceAdapter) SpotSymbol(base string) string { return strings.ToUpper(base) + "USDT" }

// SpotPrice GET {APIBase}/v3/ticker/price?symbol={BASE}USDT price。
func (a *binanceAdapter) SpotPrice(ctx context.Context, base string) (float64, error) {
	u := fmt.Sprintf("%s/v3/ticker/price?symbol=%s", strings.TrimRight(a.c.BaseURL, "/"), a.SpotSymbol(base))
	b, err := httpGet(ctx, a.c.Client, u)
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

// SigmaDaily fapi 日 K（baseURL api→fapi 对换约定——采集同法先例）；σ 引擎
// 走 Cred.SigmaFn 注入（platform 叶包禁逆向 import exchange）。
func (a *binanceAdapter) SigmaDaily(ctx context.Context, base string, days int) (float64, error) {
	if a.c.SigmaFn == nil {
		return 0, fmt.Errorf("binance σ 引擎未注入（Cred.SigmaFn 缺）")
	}
	fapi := strings.Replace(a.c.BaseURL, "api.", "fapi.", 1)
	return a.c.SigmaFn(ctx, a.c.Client, fapi, "binance", a.SpotSymbol(base), days)
}
