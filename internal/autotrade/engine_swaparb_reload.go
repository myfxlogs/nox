// engine_swaparb_reload.go —— D-515 S5：swaparb_allow_combos 表热读快照。
// 读点 swapArbAllowList/swapArbDenyList（atomic Load，nil→cfg 启动值）vs 写点
// ReloadSwapArbAllowCombos（atomic Store 整体替换），全仿 engine_xasset_reload.go。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"sync/atomic"

	"arb/internal/exec"
)

// swapArbComboSnapshot allow/deny 双轨快照（指针整体替换）。
type swapArbComboSnapshot struct {
	allow []exec.ComboAllowSpec
	deny  []exec.ComboAllowSpec
}

// swapArbAllowList/swapArbDenyList 读侧：快照优先；nil（零值 Engine 直构测试路径）回退 cfg。
func (e *Engine) swapArbAllowList() []exec.ComboAllowSpec {
	if p := e.swapArb.Load(); p != nil {
		return p.allow
	}
	return e.cfg.SwapArbAllowCombos
}

func (e *Engine) swapArbDenyList() []exec.ComboAllowSpec {
	if p := e.swapArb.Load(); p != nil {
		return p.deny
	}
	return e.cfg.SwapArbDenyCombos
}

// SetSwapArbSnapshot 注入共享快照指针（engine.New 装配；nil 安全回退 cfg）。
func (g *Gate) SetSwapArbSnapshot(p *atomic.Pointer[swapArbComboSnapshot]) { g.swapArb = p }

// swapArbLists Gate 侧同款读侧（共享指针注入；未接线回退 cfg）。
func (g *Gate) swapArbLists() (allow, deny []exec.ComboAllowSpec) {
	if g.swapArb != nil {
		if p := g.swapArb.Load(); p != nil {
			return p.allow, p.deny
		}
	}
	return g.cfg.SwapArbAllowCombos, g.cfg.SwapArbDenyCombos
}

// ReloadSwapArbAllowCombos D-515 S5：重跑 LoadSwapArbAllowCombos+共享映射
// （exec.SwapArbSpecsFromRows 单源），atomic 指针整体替换。失败保留旧快照（不致命）。
func (e *Engine) ReloadSwapArbAllowCombos(ctx context.Context) (allow []exec.ComboAllowSpec, err error) {
	allowRows, denyRows, err := e.store.LoadSwapArbAllowCombos(ctx)
	if err != nil {
		return nil, fmt.Errorf("reload swaparb allow combos: %w", err)
	}
	allow = exec.SwapArbSpecsFromRows(allowRows)
	e.swapArb.Store(&swapArbComboSnapshot{
		allow: allow,
		deny:  exec.SwapArbSpecsFromRows(denyRows),
	})
	log.Printf("[whitelist] swaparb combos reload: allow=%d deny=%d (D-515 表化热读)", len(allowRows), len(denyRows))
	return allow, nil
}
