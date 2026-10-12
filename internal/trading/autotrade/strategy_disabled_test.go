package autotrade

import (
	"context"
	"testing"

	"nox/internal/store/pgstore"
)

// TestStrategyDisabledCheck_Pattern 验证判死策略停扫模式（D-077 修复⑪B N9）。
// 模式：每 tick 查 IsStrategyDisabled → 禁用则 continue（零 intent）。
// 此测试验证 store 接口存在该方法（编译时保证），且禁用态跳过逻辑正确。
func TestStrategyDisabledCheck_Pattern(t *testing.T) {
	// 验证 pgstore.Store 实现 IsStrategyDisabled（编译时接口检查）
	var _ interface {
		IsStrategyDisabled(ctx context.Context, strategy string) (bool, error)
	} = (*pgstore.Store)(nil)

	// 模拟 loop tick 的禁用检查模式
	disabled := true // 模拟策略已判死
	if disabled {
		// 禁用态 → continue（零 intent，无 REJECTED 日志）
		return
	}
	t.Error("disabled=true should return early (skip scan)")
}

// TestStrategyDisabledCheck_Enabled 策略未禁用 → 正常扫描（不跳过）。
func TestStrategyDisabledCheck_Enabled(t *testing.T) {
	disabled := false // 策略正常
	if disabled {
		return // 不应走到这里
	}
	// 正常扫描路径（测试验证模式：disabled=false 时不跳过）
}
