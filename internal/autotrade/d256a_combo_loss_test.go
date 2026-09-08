// D-256-A S4 单测：组合级止损计数（reason-aware）。
// 验证 CountComboStopLosses 查询 + combo.FromOppID 归并 + limit 拦截逻辑。
package autotrade

import (
	"testing"

	"arb/internal/combo"
)

// TestD256A_S4_ComboLossCountsMergeOppIDs combo.FromOppID 归并旧/新格式 oppID 到 ComboKey。
// 旧格式（带时间戳尾段）和新格式（ComboKey 本身）都归并到同一 ComboKey。
func TestD256A_S4_ComboLossCountsMergeOppIDs(t *testing.T) {
	// 模拟 CountComboStopLosses 返回的 map[oppID]count
	oppCounts := map[string]int64{
		"CARRY:AUDJPY:52993526:mt5:277842155:mt5":            2, // 新格式（=ComboKey）
		"CARRY:AUDJPY:52993526:mt5:277842155:mt5:1694123456": 1, // 旧格式（尾段时间戳）
	}
	ck := "CARRY:AUDJPY:52993526:mt5:277842155:mt5"

	// 归并逻辑（同 swapArbComboLossCounts）
	out := make(map[string]int64)
	for oppID, cnt := range oppCounts {
		merged := combo.FromOppID(oppID)
		out[merged] += cnt
	}

	got := out[ck]
	if got != 3 {
		t.Errorf("combo loss count for %s: got %d, want 3 (2 new + 1 old merged)", ck, got)
	}
}

// TestD256A_S4_LimitZeroDisabled SwapArbComboLossLimit=0 → nil（关闭）。
func TestD256A_S4_LimitZeroDisabled(t *testing.T) {
	e := &Engine{cfg: Config{SwapArbComboLossLimit: 0, SwapArbComboLossWindowDays: 30}}
	// swapArbComboLossCounts 需要 store，但 limit=0 时短路返回 nil 不查 store
	got := e.swapArbComboLossCounts(nil)
	if got != nil {
		t.Errorf("SwapArbComboLossLimit=0: got %v, want nil (disabled)", got)
	}
}

// TestD256A_S4_WindowZeroDisabled SwapArbComboLossWindowDays=0 → nil（关闭）。
func TestD256A_S4_WindowZeroDisabled(t *testing.T) {
	e := &Engine{cfg: Config{SwapArbComboLossLimit: 3, SwapArbComboLossWindowDays: 0}}
	got := e.swapArbComboLossCounts(nil)
	if got != nil {
		t.Errorf("SwapArbComboLossWindowDays=0: got %v, want nil (disabled)", got)
	}
}

// TestD256A_R2_StripStrategyPrefix stripStrategyPrefix 剥策略前缀。
// combo.Key = strategy:canonical:venueBuy:venueSell → canonical:venueBuy:venueSell。
func TestD256A_R2_StripStrategyPrefix(t *testing.T) {
	cases := []struct{ in, want string }{
		{"CARRY:AUDJPY:vA:mt5:vB:mt5", "AUDJPY:vA:mt5:vB:mt5"},
		{"SWAPARB:USDJPY:vA:mt5:vB:mt5", "USDJPY:vA:mt5:vB:mt5"},
		{"CROSS_VENUE:EURUSD:ic:mt5:pxb:mt5", "EURUSD:ic:mt5:pxb:mt5"},
		{"noColon", "noColon"}, // 异常输入原样返回
	}
	for _, c := range cases {
		got := stripStrategyPrefix(c.in)
		if got != c.want {
			t.Errorf("stripStrategyPrefix(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

// TestD256A_R2_CrossLabelLossCountMerge D-256-A R2：CARRY 标签止损计数与 SWAPARB 标签
// 同配对归并到同一键（剥策略前缀后），跨标签循环保护生效。
func TestD256A_R2_CrossLabelLossCountMerge(t *testing.T) {
	// 模拟 CountComboStopLosses 返回的 map[oppID]count
	// CARRY 标签 2 次 + SWAPARB 标签 1 次，同配对 AUDJPY:vA:mt5:vB:mt5
	oppCounts := map[string]int64{
		"CARRY:AUDJPY:vA:mt5:vB:mt5":   2,
		"SWAPARB:AUDJPY:vA:mt5:vB:mt5": 1,
	}
	wantKey := "AUDJPY:vA:mt5:vB:mt5"

	// 归并逻辑（同 swapArbComboLossCounts，含 stripStrategyPrefix）
	out := make(map[string]int64)
	for oppID, cnt := range oppCounts {
		ck := combo.FromOppID(oppID)
		ck = stripStrategyPrefix(ck)
		out[ck] += cnt
	}

	got := out[wantKey]
	if got != 3 {
		t.Errorf("R2 cross-label merge: key %s count=%d, want 3 (2 CARRY + 1 SWAPARB merged)", wantKey, got)
	}
	// 验证查表键不含策略前缀——CARRY 候选用 stripStrategyPrefix 后能命中
	carryCk := stripStrategyPrefix("CARRY:AUDJPY:vA:mt5:vB:mt5")
	if carryCk != wantKey {
		t.Errorf("R2 lookup key: CARRY stripped=%q, want %q", carryCk, wantKey)
	}
	swaparbCk := stripStrategyPrefix("SWAPARB:AUDJPY:vA:mt5:vB:mt5")
	if swaparbCk != wantKey {
		t.Errorf("R2 lookup key: SWAPARB stripped=%q, want %q", swaparbCk, wantKey)
	}
}
