// engine_setters.go —— 注入面 setters（从 engine.go 拆出控行数 <300；D-555 S1 起
// 含 cexPaperHook——双CEX 纸面轨候选钩子）。
package engine

import (
	"context"

	"arb/internal/detector"
)

// SetAssetEquiv 注入标的等价类视图（D-300 裁 5 单实例共享）。
// nil=关闭跨标的功能——快照不扩展，存量 detector 行为不变。
func (e *Engine) SetAssetEquiv(v AssetEquivView) {
	e.assetEquiv = v
}

// SetExcludeCanonicals 注入检测产出排除集合（D-072 资金提醒弹窗提示词 B1）。
// 复用 ARB_AUTOTRADE_EXCLUDE_CANONICALS（执行层+检测层双用途，P3 单一真相）；nil/空 = 不排除。
// main 在 config 加载后注入；命中 canonical 的机会不入库（从源头消除假机会）。
func (e *Engine) SetExcludeCanonicals(set map[string]bool) {
	e.excludeCanonicals = set
}

// SetCexPaperHook D-555 S1：注入双CEX 纸面轨候选钩子（cmd/core 装配：
// FUNDING_HEDGE 候选 → scan.StepPaperCexCarry；nil=未接线）。
func (e *Engine) SetCexPaperHook(h func(context.Context, []detector.Candidate)) {
	e.cexPaperHook = h
}

// SetDisabledStrategies 注入自动淘汰策略集合（§1 自动淘汰闭环）。
// strategy_stats.disabled=true 的策略产出的机会不入库——系统已自证亏损，展示假机会误导。
// main 定期刷新（淘汰判定在运行时触发）；nil/空 = 不排除。
func (e *Engine) SetDisabledStrategies(set map[string]bool) {
	e.disabledMu.Lock()
	e.disabledStrategies = set
	e.disabledMu.Unlock()
}

// isStrategyDisabled 线程安全读取 disabledStrategies。
func (e *Engine) isStrategyDisabled(strategy string) bool {
	e.disabledMu.RLock()
	defer e.disabledMu.RUnlock()
	return e.disabledStrategies[strategy]
}
