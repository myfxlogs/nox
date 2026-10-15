// dci_sigma.go —— D-526：σ_daily 提案层取件（venue 日 K 实算，
// 统计参考用途 D-004 拉取豁免；窗口期 app_config `dci_sigma_window_days` 可调，
// 缺省 28d；源全败 → err fail-closed 不产提案）。
package oppmatch

import (
	"context"
	"fmt"
	"strings"

	"nox/internal/data/collect/exchange"
	"nox/internal/platform/httpx"
	"nox/internal/store/pgstore"
)

// dciSigmaSources venue 序（标的 BTC σ 同一——先后序只是可用性兜底）。
var dciSigmaSources = []struct {
	venue, symbol string
}{
	{"binance", "BTCUSDT"},
	{"okx", "BTC-USDT"},
}

// sigmaFn 测试注口（nil=dciSigma venue 实读）。
var sigmaFn func(ctx context.Context, st *pgstore.Store) (float64, string, error)

// fetchSigma σ 读取（注口优先）。
func fetchSigma(ctx context.Context, st *pgstore.Store) (float64, string, error) {
	if sigmaFn != nil {
		return sigmaFn(ctx, st)
	}
	return dciSigma(ctx, st)
}

// dciSigma σ_daily 取件：exchange_configs 各源 venue base_url 实读逐源试；
// 全败 → err。返回 (σ, 源说明)。
func dciSigma(ctx context.Context, st *pgstore.Store) (float64, string, error) {
	days := int64(28)
	if v, ok, err := st.AppConfigInt64(ctx, "dci_sigma_window_days"); err == nil && ok && v >= 3 {
		days = v
	}
	cfgs, err := st.ListExchangeConfigs(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("exchange_configs 读败：%w", err)
	}
	baseOf := map[string]string{}
	for _, c := range cfgs {
		if c.Simulated || c.BaseURL == "" {
			continue
		}
		if baseOf[c.Venue] == "" {
			baseOf[c.Venue] = c.BaseURL
		}
	}
	cl := httpx.Client()
	var fails []string
	for _, src := range dciSigmaSources {
		base := baseOf[src.venue]
		if src.venue == "binance" {
			base = strings.Replace(base, "api.", "fapi.", 1) // spot 基址→fapi（klines 永续面）
		}
		if base == "" {
			continue
		}
		s, err := exchange.SigmaDaily(ctx, cl, base, src.venue, src.symbol, int(days))
		if err != nil {
			fails = append(fails, fmt.Sprintf("%s:%v", src.venue, err))
			continue
		}
		if s <= 0 {
			fails = append(fails, src.venue+":σ=0")
			continue
		}
		return s, fmt.Sprintf("%s %dd 日K", src.venue, days), nil
	}
	return 0, "", fmt.Errorf("σ 源全败（%s）——fail-closed 不产提案", strings.Join(fails, "；"))
}
