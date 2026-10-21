// gate.go —— gate 定价适配器（/api/v4/spot/tickers last + K 线 σ）。
// gate base_url 库内约定已含 /api/v4 前缀（reconcile/earn_rail_gate 实证）。
package venuepricer

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

type gateAdapter struct{ c Cred }

func init() { Register("gate", func(c Cred) Adapter { return &gateAdapter{c} }) }

func (a *gateAdapter) SpotSymbol(base string) string { return strings.ToUpper(base) + "_USDT" }

// SpotPrice GET {base}/api/v4/spot/tickers?currency_pair={BASE}_USDT rows[0].last
// （cex_executor_spot_cash 同径先例）。base 未含 /api/v4 前缀时补齐。
func (a *gateAdapter) SpotPrice(ctx context.Context, base string) (float64, error) {
	api := strings.TrimRight(a.c.BaseURL, "/")
	if !strings.Contains(api, "/api/v4") {
		api += "/api/v4"
	}
	u := fmt.Sprintf("%s/spot/tickers?currency_pair=%s", api, a.SpotSymbol(base))
	b, err := httpGet(ctx, a.c.Client, u)
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
		return 0, fmt.Errorf("gate tickers %s: empty", a.SpotSymbol(base))
	}
	return strconv.ParseFloat(rows[0].Last, 64)
}

func (a *gateAdapter) SigmaDaily(ctx context.Context, base string, days int) (float64, error) {
	if a.c.SigmaFn == nil {
		return 0, fmt.Errorf("gate σ 引擎未注入（Cred.SigmaFn 缺）")
	}
	return a.c.SigmaFn(ctx, a.c.Client, a.c.BaseURL, "gate", a.SpotSymbol(base), days)
}
