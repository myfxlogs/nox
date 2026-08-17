package detector

import (
	"context"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
)

// FundingHedgeDetector 检测现货 vs 永续的 funding rate 套利（§5.2）。
type FundingHedgeDetector struct {
	MinNetAnnualized float64
	FreshnessTTL     time.Duration
}

func (d *FundingHedgeDetector) Type() OppType { return TypeFundingHedge }

// Scan 需要 funding Fact（通过 FactMap 注入）+ QuoteBus snapshot。
// FactMap: canonical → venue → funding Fact（已年化）。
func (d *FundingHedgeDetector) Scan(_ context.Context, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate {
	var out []Candidate
	now := time.Now()
	var fundingFacts map[string]map[string]float64
	if fp != nil {
		fundingFacts = fp.FundingFacts()
	}
	for canonical, venues := range snapshot {
		spot, hasSpot := findVenueType(venues, "spot")
		perp, hasPerp := findVenueType(venues, "perp")
		if !hasSpot || !hasPerp {
			continue
		}
		if stale(spot, now, d.FreshnessTTL) || stale(perp, now, d.FreshnessTTL) {
			continue
		}
		funding, ok := lookupFunding(fundingFacts, canonical, perp.Venue)
		if !ok {
			continue
		}
		netAnnualized := funding
		if netAnnualized < d.MinNetAnnualized {
			continue
		}
		qt := spot.Time
		if perp.Time.After(qt) {
			qt = perp.Time
		}
		out = append(out, Candidate{
			Type:      TypeFundingHedge,
			Canonical: canonical,
			VenueBuy:  spot.VenueKey(),
			VenueSell: perp.VenueKey(),
			PriceBuy:  spot.Ask,
			PriceSell: perp.Bid,
			GrossBps:  netAnnualized, // 净年化作为 gross（评估器拆解后重算净值）
			QuoteTime: qt.UnixMilli(),
		})
	}
	return out
}

// CarryDetector 检测不同 venue 的 swap rate 息差（§5.3）。
type CarryDetector struct {
	MinNetBps     float64 // 年化净息差 bps 阈值
	FreshnessTTL  time.Duration
}

func (d *CarryDetector) Type() OppType { return TypeCarry }

// Scan 需要 swap Fact（通过 swapFacts 注入）。
// swapFacts: canonical → venue → {swap_long, swap_short} bps_daily。
func (d *CarryDetector) Scan(_ context.Context, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate {
	var out []Candidate
	now := time.Now()
	var swapFacts map[string]map[string][2]float64
	if fp != nil {
		swapFacts = fp.SwapFacts()
	}
	for canonical, venues := range snapshot {
		if len(venues) < 2 {
			continue
		}
		vkeys := sortedKeys(venues)
		for i := 0; i < len(vkeys); i++ {
			for j := 0; j < len(vkeys); j++ {
				if i == j {
					continue
				}
				a, b := venues[vkeys[i]], venues[vkeys[j]]
				if stale(a, now, d.FreshnessTTL) || stale(b, now, d.FreshnessTTL) {
					continue
				}
				swapsA, okA := lookupSwap(swapFacts, canonical, a.Venue)
				swapsB, okB := lookupSwap(swapFacts, canonical, b.Venue)
				if !okA || !okB {
					continue
				}
				// 买 A（swap_long A）+ 卖 B（swap_short B）：日净息差 = swap_long_A + swap_short_B
				dailyNet := swapsA[0] + swapsB[1] // [0]=long, [1]=short
				annualNet := dailyNet * 365
				if annualNet < d.MinNetBps {
					continue
				}
				qt := a.Time
				if b.Time.After(qt) {
					qt = b.Time
				}
				out = append(out, Candidate{
					Type:      TypeCarry,
					Canonical: canonical,
					VenueBuy:  a.VenueKey(),
					VenueSell: b.VenueKey(),
					PriceBuy:  a.Ask,
					PriceSell: b.Bid,
					GrossBps:  annualNet,
					QuoteTime: qt.UnixMilli(),
				})
			}
		}
	}
	return out
}

func findVenueType(venues map[string]bus.Quote, vt string) (bus.Quote, bool) {
	for _, q := range venues {
		if q.VenueType == vt {
			return q, true
		}
	}
	return bus.Quote{}, false
}

func lookupFunding(m map[string]map[string]float64, canonical, venue string) (float64, bool) {
	if vm, ok := m[canonical]; ok {
		if v, ok2 := vm[venue]; ok2 {
			return v, true
		}
	}
	return 0, false
}

func lookupSwap(m map[string]map[string][2]float64, canonical, venue string) ([2]float64, bool) {
	if vm, ok := m[canonical]; ok {
		if v, ok2 := vm[venue]; ok2 {
			return v, true
		}
	}
	return [2]float64{}, false
}

// FactToMap 将 []fact.Fact 转为 canonical → venue → value（funding 用）。
func FactToMap(facts []fact.Fact) map[string]map[string]float64 {
	m := make(map[string]map[string]float64)
	for _, f := range facts {
		if m[f.Symbol] == nil {
			m[f.Symbol] = make(map[string]float64)
		}
		m[f.Symbol][f.Venue] = f.Value
	}
	return m
}

// SwapFactsToMap 将 []fact.Fact（swap_long/swap_short）转为 canonical → venue → [long, short]。
func SwapFactsToMap(facts []fact.Fact) map[string]map[string][2]float64 {
	m := make(map[string]map[string][2]float64)
	for _, f := range facts {
		if f.Kind != fact.KindSwapLong && f.Kind != fact.KindSwapShort {
			continue
		}
		if m[f.Symbol] == nil {
			m[f.Symbol] = make(map[string][2]float64)
		}
		pair := m[f.Symbol][f.Venue]
		if f.Kind == fact.KindSwapLong {
			pair[0] = f.Value
		} else {
			pair[1] = f.Value
		}
		m[f.Symbol][f.Venue] = pair
	}
	return m
}
