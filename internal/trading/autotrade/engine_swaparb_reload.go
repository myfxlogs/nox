// engine_swaparb_reload.go —— D-515 S5：swaparb_allow_combos 表热读快照。
// 读点 swapArbAllowList/swapArbDenyList（atomic Load，nil→cfg 启动值）vs 写点
// ReloadSwapArbAllowCombos（atomic Store 整体替换），全仿 engine_xasset_reload.go。
// 快照类型与 gate.Gate 侧读侧移 internal/risk/gate（D-212 拆搬）。
package autotrade

import (
	"context"
	"fmt"
	"log"

	"nox/internal/risk/gate"
	"nox/internal/trading/exec"
)

// swapArbAllowList/swapArbDenyList 读侧：快照优先；nil（零值 Engine 直构测试路径）回退 cfg。
func (e *Engine) swapArbAllowList() []exec.ComboAllowSpec {
	if p := e.swapArb.Load(); p != nil {
		return p.Allow
	}
	return e.cfg.SwapArbAllowCombos
}

func (e *Engine) swapArbDenyList() []exec.ComboAllowSpec {
	if p := e.swapArb.Load(); p != nil {
		return p.Deny
	}
	return e.cfg.SwapArbDenyCombos
}

// ReloadSwapArbAllowCombos D-515 S5：重跑 LoadSwapArbAllowCombos+共享映射
// （exec.SwapArbSpecsFromRows 单源），atomic 指针整体替换。失败保留旧快照（不致命）。
func (e *Engine) ReloadSwapArbAllowCombos(ctx context.Context) (allow []exec.ComboAllowSpec, err error) {
	allowRows, denyRows, err := e.store.LoadSwapArbAllowCombos(ctx)
	if err != nil {
		return nil, fmt.Errorf("reload swaparb allow combos: %w", err)
	}
	allow = exec.SwapArbSpecsFromRows(allowRows)
	e.swapArb.Store(&gate.SwapArbComboSnapshot{
		Allow: allow,
		Deny:  exec.SwapArbSpecsFromRows(denyRows),
	})
	log.Printf("[whitelist] swaparb combos reload: allow=%d deny=%d (D-515 表化热读)", len(allowRows), len(denyRows))
	return allow, nil
}
