// D-256-A S2 单测：CARRY/SWAPARB 风险族合并淘汰。
// riskFamily 映射 + AggregateStrategyStatsByEnvironmentFamily 族级聚合 + settlement.Settle family 注入。
package autotrade

import (
	"testing"
)

// TestD256A_S2_RiskFamily_CarrySwapArbSameFamily CARRY/SWAPARB 互为同族。
func TestD256A_S2_RiskFamily_CarrySwapArbSameFamily(t *testing.T) {
	carryFam := riskFamily("CARRY")
	swapFam := riskFamily("SWAPARB")
	if len(carryFam) != 2 || carryFam[0] != "CARRY" || carryFam[1] != "SWAPARB" {
		t.Errorf("CARRY family: want [CARRY SWAPARB], got %v", carryFam)
	}
	if len(swapFam) != 2 || swapFam[0] != "CARRY" || swapFam[1] != "SWAPARB" {
		t.Errorf("SWAPARB family: want [CARRY SWAPARB], got %v", swapFam)
	}
}

// TestD256A_S2_RiskFamily_OtherStrategiesSingleton 其他策略自成一族。
func TestD256A_S2_RiskFamily_OtherStrategiesSingleton(t *testing.T) {
	for _, s := range []string{"CROSS_VENUE", "MEANREVERT", "FUNDING_HEDGE", "PERP_FUNDING"} {
		fam := riskFamily(s)
		if len(fam) != 1 || fam[0] != s {
			t.Errorf("strategy %s: want singleton [%s], got %v", s, s, fam)
		}
	}
}

// TestD256A_S2_FamilyResolverAdapter_Delegates FamilyResolverAdapter 委托 riskFamily。
func TestD256A_S2_FamilyResolverAdapter_Delegates(t *testing.T) {
	got := FamilyResolverAdapter("CARRY")
	want := riskFamily("CARRY")
	if len(got) != len(want) {
		t.Errorf("FamilyResolverAdapter: want %v, got %v", want, got)
	}
}
