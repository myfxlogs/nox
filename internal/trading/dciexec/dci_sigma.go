// dci_sigma.go —— D-526：执行层 σ_daily 取件（D-651 A 段收敛 venuepricer：
// 按提案 venue 取该所适配器——禁 binance 先兜，okx 产品用 binance σ=跨所
// 波动率口径差病灶；venue 无 σ 径/读败=fail-closed 如实）。
// 窗口期 app_config `dci_sigma_window_days`（缺省 28d；<3 按缺省）。
package dciexec

import (
	"context"
	"fmt"
)

// sigmaDaily 提案 venue 的 σ_daily 实算；SigmaFn 测试注口（nil=venue 实读）。
func (e *Executor) sigmaDaily(ctx context.Context, venue string) (float64, error) {
	if e.SigmaFn != nil {
		return e.SigmaFn(ctx)
	}
	days := int64(28)
	if e.Store != nil {
		if v, ok, err := e.Store.AppConfigInt64(ctx, "dci_sigma_window_days"); err == nil && ok && v >= 3 {
			days = v
		}
	}
	a, err := e.venuePricer(venue)
	if err != nil {
		return 0, fmt.Errorf("σ 源 %s 凭证缺口：%v", venue, err)
	}
	sig, err := a.SigmaDaily(ctx, "BTC", int(days))
	if err != nil {
		return 0, fmt.Errorf("%s σ 读败：%v", venue, err)
	}
	if sig <= 0 {
		return 0, fmt.Errorf("%s σ=0", venue)
	}
	return sig, nil
}
