// engine_econ_pair.go —— D-018 econ 的 pair-key/venue 分类 helpers（自 engine_econ.go
// 拆出，纯搬移零逻辑变更；D-024 拆分 ≤300 契约）。
package autotrade

import (
	"context"
	"log"
	"strings"

	"arb/internal/store/pgstore"
)

// xassetPairKey D-423 三元组化：pairKey 解析（CanonicalSell 优先）+ (cexVenue,mtVenue)
// 两维——venue 取仓 OPEN 腿（:mt4/:mt5 后缀判 MT 腿余为 CEX 腿），仓腿缺→机会
// VenueBuy/VenueSell 补；非 MT 形态 mtVenue=""=查询自然 miss 诚实缺。自 fee_amortize 收编。
func (e *Engine) xassetPairKey(ctx context.Context, p *pgstore.Position) (pair, cexVenue, mtVenue string, ok bool) {
	opp, err := e.store.GetOpportunity(ctx, p.OpportunityID)
	if err != nil || opp == nil {
		return "", "", "", false
	}
	if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		pair = *opp.CanonicalSell + "-" + strings.TrimSuffix(opp.Canonical, "USD")
	} else {
		pair = canonicalFromOppID(p.OpportunityID)
	}
	cexVenue, mtVenue = xassetLegVenues(opp.VenueBuy, opp.VenueSell)
	if legs, lerr := e.store.ListPositionLegs(ctx, p.ID); lerr == nil {
		for _, l := range legs {
			if l.Phase != "OPEN" {
				continue
			}
			if isMTVenue(l.Venue) {
				mtVenue = l.Venue
			} else {
				cexVenue = l.Venue
			}
		}
	}
	return pair, cexVenue, mtVenue, true
}

// econEntryGate D-014 ①入场闸：XASSET_CARRY/FUNDING_HEDGE 在 sizing 后调——
// CarryPerDay>0 ∧ BreakevenDays≤MaxBreakevenDays（缺省 3.0）才放行。
// 装配缺账（carry facts/费率/MT 报价任一缺）→ fail-closed 拒开；不产 intent 不落 REJECTED
// （同 D-311 静默先例——机会仍在簿内，账齐即自然放行）。true=拦截。
func (e *Engine) econEntryGate(ctx context.Context, opp pgstore.Opportunity, legNotionalUSD float64) bool {
	if opp.Type != "XASSET_CARRY" && opp.Type != "FUNDING_HEDGE" {
		return false
	}
	sellCanonical := opp.Canonical
	if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		sellCanonical = *opp.CanonicalSell
	}
	snap, err := e.econEntrySnapshot(ctx, opp, sellCanonical, legNotionalUSD)
	maxBE := e.cfg.MaxBreakevenDays
	if maxBE <= 0 {
		maxBE = 3.0
	}
	if err != nil {
		log.Printf("[autotrade] skip %s: econ 装配缺账 fail-closed: %v", opp.ID, err)
		return true
	}
	if !snap.EntryOK(maxBE) {
		log.Printf("[autotrade] skip %s: econ breakeven %.2fd > cap %.1fd (carry $%.2f/d entry $%.2f exit $%.2f)",
			opp.ID, snap.BreakevenDays(), maxBE, snap.CarryPerDayUSD, snap.EntryCostUSD, snap.ExitCostEstUSD)
		return true
	}
	return false
}
