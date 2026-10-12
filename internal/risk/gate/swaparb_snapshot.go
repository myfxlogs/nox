// swaparb_snapshot.go —— swaparb_allow_combos 表热读快照共享件
// （engine_swaparb_reload.go 拆分，D-212 拆搬；Engine 侧读写仍在其文件内）。
package gate

import (
	"sync/atomic"

	"arb/internal/trading/exec"
)

// SwapArbComboSnapshot allow/deny 双轨快照（指针整体替换）。
type SwapArbComboSnapshot struct {
	Allow []exec.ComboAllowSpec
	Deny  []exec.ComboAllowSpec
}

// SetSwapArbSnapshot 注入共享快照指针（engine.New 装配；nil 安全回退 cfg）。
func (g *Gate) SetSwapArbSnapshot(p *atomic.Pointer[SwapArbComboSnapshot]) { g.swapArb = p }

// swapArbLists Gate 侧同款读侧（共享指针注入；未接线回退 cfg）。
func (g *Gate) swapArbLists() (allow, deny []exec.ComboAllowSpec) {
	if g.swapArb != nil {
		if p := g.swapArb.Load(); p != nil {
			return p.Allow, p.Deny
		}
	}
	return g.cfg.SwapArbAllowCombos, g.cfg.SwapArbDenyCombos
}
