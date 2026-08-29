// broker_symbol_map 规则库自动补录（D-140 S1）。
package pgstore

import (
	"context"
	"fmt"
	"log"

	"arb/internal/normalize"
)

// BackfillRuleMappings 规则库自动补录（D-140 S1）。
// 扫描 venue 下全部已知 broker_symbol，exact 未命中的跑规则库，
// 产出候选清单先落表 source='rule'（canonical 归一即生效，CS 留 NULL=CS 门继续拦）。
// 已有映射（含 source='rule'/'manual'/'meta'）跳过——幂等。
// 返回新增行数。
func (s *Store) BackfillRuleMappings(ctx context.Context, venue string, brokerSymbols []string, eng *normalize.RuleEngine) (int, error) {
	if eng == nil || len(brokerSymbols) == 0 {
		return 0, nil
	}
	existing, err := s.ListSymbolMappings(ctx, venue)
	if err != nil {
		return 0, fmt.Errorf("list existing mappings: %w", err)
	}
	mapped := make(map[string]bool, len(existing))
	for _, m := range existing {
		mapped[m.BrokerSymbol] = true
	}
	inserted := 0
	for _, sym := range brokerSymbols {
		if mapped[sym] {
			continue // 幂等：已有映射跳过
		}
		canon, ok := eng.Match(sym)
		if !ok {
			continue // 规则未命中，留待人工
		}
		if err := s.UpsertSymbolMapping(ctx, &SymbolMapping{
			Venue:        venue,
			BrokerSymbol: sym,
			Canonical:    canon,
			ContractSize: nil, // CS 留 NULL=CS 门继续拦（红线：规则不产 CS）
			Source:       "rule",
		}); err != nil {
			log.Printf("[normalize] backfill %s/%s: %v", venue, sym, err)
			continue
		}
		inserted++
	}
	return inserted, nil
}
