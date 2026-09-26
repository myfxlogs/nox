// service_execute_anchor.go —— D-378a S1：XASSET 锚纯 quote 口径（493 毒锚修复）。
// 旧径 EnsureEntrySpreadFromLegs=fill 口径（(buyActual−sellActual)/buyActual）吸收 venue
// 成交噪声——demo 簿 ±14bps 漂移下「含滑点=保守偏早安全」假设反转成必死毒锚。
// quote 口径与 engine.currentBasisBps 同公式纯 quote 版（无 PriceActual 回退——锚必须与 fill 无关，
// 这正是修复点）：spread=buyBid−sellAsk，ref=buyAsk，anchor=spread/ref×1e4。
package dashboard

import (
	"arb/internal/keydomain"
	"context"

	"arb/internal/store/pgstore"
)

// quoteEntrySpreadBps XASSET 锚纯 quote 计算（调用方经 UpdateExpectedEntrySpreadBps 落列，
// IS NULL 幂等同径）。canonical=买腿 canonical（写点作用域变量，engine 同键）；
// opp=机会行（自动路径写点作用域已有，手动路径 nil→走 symbol_map 反查）。
func (s *Service) quoteEntrySpreadBps(ctx context.Context, posID int64, canonical string, opp *pgstore.Opportunity) (float64, bool) {
	if s.store == nil || s.qbus == nil {
		return 0, false
	}
	legs, err := s.store.ListPositionLegs(ctx, posID)
	if err != nil {
		return 0, false
	}
	var buyLeg, sellLeg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase != "OPEN" {
			continue
		}
		if legs[i].Side == "BUY" {
			buyLeg = &legs[i]
		} else {
			sellLeg = &legs[i]
		}
	}
	if buyLeg == nil || sellLeg == nil {
		return 0, false
	}
	sellCanonical := s.xassetSellCanonicalFor(ctx, opp, sellLeg)
	if sellCanonical == "" {
		return 0, false // 跨 canonical 解析不出 → fail-closed 不写锚（engine 同语义）
	}
	// D-481：CEX 腿 qbus 键=原生 symbol 归一采集键（oppID canonical 错位永不命中），
	// LatestLeg 原生键优先+canonical 兜底；MT 腿 canonical 直查。
	buyQ, ok1 := s.qbus.LatestLeg(canonical, buyLeg.Symbol, buyLeg.Venue)
	sellQ, ok2 := s.qbus.LatestLeg(sellCanonical, sellLeg.Symbol, sellLeg.Venue)
	if !ok1 || !ok2 {
		return 0, false // 任一腿 quote 缺失 → 不写锚（留 NULL=fail-closed 不决策）
	}
	ref := buyQ.Ask
	if ref <= 0 {
		return 0, false
	}
	return (buyQ.Bid - sellQ.Ask) / ref * 1e4, true
}

// xassetSellCanonicalFor D-378a S1：复现 engine_position_xasset.go xassetSellCanonical 语义链——
// opp.CanonicalSell 优先（自动路径写点作用域已有 opp 对象，零新查询）；缺失 → 卖腿 venue 裸名
// 经 broker_symbol_map 反查 sellLeg.Symbol；皆无 → ""（fail-closed，不拿买 canonical 错查）。
func (s *Service) xassetSellCanonicalFor(ctx context.Context, opp *pgstore.Opportunity, sellLeg *pgstore.PositionLeg) string {
	if opp != nil && opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		return *opp.CanonicalSell
	}
	bare := keydomain.BareOf(sellLeg.Venue)
	maps, err := s.store.ListSymbolMappings(ctx, bare)
	if err != nil {
		return ""
	}
	for _, m := range maps {
		if m.BrokerSymbol == sellLeg.Symbol && m.Canonical != "" {
			return m.Canonical
		}
	}
	return ""
}
