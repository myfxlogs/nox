// engine_helpers.go —— 自 engine.go 拆出（300 行棘轮；纯搬移零语义改）。
package engine

import (
	"nox/internal/data/bus"
	"nox/internal/data/keydomain"
	"nox/internal/research/evaluator"
	"nox/internal/store/pgstore"
)

func resultToOpp(r *evaluator.Result) *pgstore.Opportunity {
	// 跨标的配对列——空串转 NULL（非 XASSET_CARRY 不留空串）。
	var canonicalSell, assetClass *string
	if r.CanonicalSell != "" {
		canonicalSell = &r.CanonicalSell
	}
	if r.AssetClass != "" {
		assetClass = &r.AssetClass
	}
	return &pgstore.Opportunity{
		ID:             r.ID,
		Type:           r.Type,
		Canonical:      r.Canonical,
		CanonicalSell:  canonicalSell,
		AssetClass:     assetClass,
		VenueBuy:       r.VenueBuy,
		VenueSell:      r.VenueSell,
		PriceBuy:       r.PriceBuy,
		PriceSell:      r.PriceSell,
		GrossBps:       r.GrossBps,
		SpreadCost:     r.SpreadCost,
		CommissionCost: r.CommissionCost,
		SlippageCost:   r.SlippageCost,
		SwapCost:       r.SwapCost,
		NetBps:         r.NetBps,
		BreakevenDays:  r.BreakevenDays,
		QuoteTime:      r.QuoteTime,
		ExpiresAt:      r.ExpiresAt,
	}
}

func findQuote(snapshot map[string]map[string]bus.Quote, canonical, venueKey string) *bus.Quote {
	venues, ok := snapshot[canonical]
	if !ok {
		return nil
	}
	// venueKey 格式 "{venue}:{venueType}"
	for _, q := range venues {
		if keydomain.Join(q.Venue, q.VenueType) == venueKey {
			return &q
		}
	}
	return nil
}
