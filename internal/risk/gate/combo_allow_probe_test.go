// combo_allow_probe_test.go —— comboAllowVerdict PROBE 豁免锚（engine_probe_test 拆出，D-212 拆搬）。
package gate

import (
	"testing"

	"nox/internal/store/pgstore"
)

// TestComboAllowVerdict_ProbeNotCounted D-181 S2：probe 仓不计 MAX_ACTIVE_COMBOS。
func TestComboAllowVerdict_ProbeNotCounted(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "AUDJPY", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	// 1 个常规仓 + 1 个 probe 仓，maxActive=1 → 常规仓计数 1 >= 1 应拒，
	// 但 probe 仓不计 → activeCount=1（常规）>= 1 → 仍拒。
	// 改为 maxActive=2 → activeCount=1（常规）< 2 → 放行（probe 不占额度）。
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:AUDJPY:A:mt5:B:mt5", Note: ""},
		{OpportunityID: "SWAPARB:AUDJPY:A:mt5:B:mt5", Note: "PROBE"},
	}
	v := comboAllowVerdict(nil, nil, true, 2, 0, intent, active)
	if !v.Pass {
		t.Errorf("probe not counted: want pass (activeCount=1 < maxActive=2), got reject: %s", v.Reason)
	}
}

// TestComboAllowVerdict_RegularStillCounted D-181 S2 反证：常规仓仍计数。
func TestComboAllowVerdict_RegularStillCounted(t *testing.T) {
	intent := TradeIntent{Strategy: "SWAPARB", Canonical: "AUDJPY", VenueBuy: "A:mt5", VenueSell: "B:mt5"}
	active := []pgstore.Position{
		{OpportunityID: "SWAPARB:AUDJPY:A:mt5:B:mt5", Note: ""}, // 常规仓计数
	}
	v := comboAllowVerdict(nil, nil, true, 1, 0, intent, active)
	if v.Pass {
		t.Error("regular counted: want reject (activeCount=1 >= maxActive=1), got pass")
	}
}
