// dci_sigma.go —— D-526：执行层 σ_daily 取件（venue 日 K REST，统计参考用途；
// 源序 binance fapi klines→okx history-candles，全败→err fail-closed 不打 venue）。
// 窗口期 app_config `dci_sigma_window_days`（缺省 28d；<3 按缺省）。
package dciexec

import (
	"context"
	"fmt"
	"strings"

	"nox/internal/data/collect/exchange"
)

// sigmaDaily 标的 BTC σ_daily 实算；SigmaFn 测试注口（nil=venue 实读）。
func (e *Executor) sigmaDaily(ctx context.Context) (float64, error) {
	if e.SigmaFn != nil {
		return e.SigmaFn(ctx)
	}
	days := int64(28)
	if e.Store != nil {
		if v, ok, err := e.Store.AppConfigInt64(ctx, "dci_sigma_window_days"); err == nil && ok && v >= 3 {
			days = v
		}
	}
	type src struct{ venue, base, sym string }
	var srcs []src
	// binance：spot api 基址→fapi（采集同法 replace 先例——baseURL fapi↔api 对换约定）。
	if b := strings.Replace(e.APIBase, "api.", "fapi.", 1); b != e.APIBase {
		srcs = append(srcs, src{"binance", b, "BTCUSDT"})
	}
	if c, ok := e.EarnCreds["okx"]; ok && c.BaseURL != "" {
		srcs = append(srcs, src{"okx", c.BaseURL, "BTC-USDT"})
	}
	var fails []string
	for _, s := range srcs {
		sig, err := exchange.SigmaDaily(ctx, e.Client, s.base, s.venue, s.sym, int(days))
		if err != nil {
			fails = append(fails, fmt.Sprintf("%s:%v", s.venue, err))
			continue
		}
		if sig <= 0 {
			fails = append(fails, s.venue+":σ=0")
			continue
		}
		return sig, nil
	}
	return 0, fmt.Errorf("σ 源全败（%s）", strings.Join(fails, "；"))
}
