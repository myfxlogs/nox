package dashboard

import (
	"context"
	"log"

	"nox/internal/data/adapter"
	"nox/internal/store/pgstore"
)

// unitSensitiveSet D-594：单位敏感 canonical 集——DB 派生（任一标定行 scale≠1）
// ∪ 静态种子 XCUUSD（迁移未跑/行缺失时铜仍 fail-closed）。查询失败不阻塞，按种子集继续。
func (m *BrokerManager) unitSensitiveSet(ctx context.Context, venue string) map[string]bool {
	sensitive := map[string]bool{"XCUUSD": true}
	sens, err := m.store.ListUnitSensitiveCanonicals(ctx)
	if err != nil {
		log.Printf("[broker] %s unit sensitive list: %v（回落种子集）", venue, err)
		return sensitive
	}
	for c := range sens {
		sensitive[c] = true
	}
	return sensitive
}

// unitScalePlan D-594 S2/S4：从映射行派生 scale 注入面（仅双列均已标定的行），并把
// 单位敏感 canonical 的未标定行并入 blocked——保留订阅（native 帧照发=单位证据仍落地），
// canonical 槽禁占（发射口 emitPlan 对 sensitive+未标定另有同款兜底）。
func unitScalePlan(rows []pgstore.SymbolMapping, sensitive map[string]bool, blocked map[string]bool) map[string]adapter.SymbolScale {
	scales := make(map[string]adapter.SymbolScale)
	for _, mp := range rows {
		switch {
		case mp.PriceScale != nil && mp.QtyScale != nil:
			scales[mp.BrokerSymbol] = adapter.SymbolScale{PriceScale: *mp.PriceScale, QtyScale: *mp.QtyScale}
		case sensitive[mp.Canonical]:
			blocked[mp.BrokerSymbol] = true
		}
	}
	return scales
}
