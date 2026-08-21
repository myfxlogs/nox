package detector

import (
	"context"
	"time"

	"arb/internal/bus"
)

// PerpFundingDetector 检测跨所 perp funding rate 价差（D-064）。
// 同一 canonical 在两个 CEX 的 perp funding rate 不同 → 高 funding 端持空（收）、
// 低 funding 端持多（付），净收 funding 差（年化百分点）。
type PerpFundingDetector struct {
	MinNetAnnualized float64 // 年化净 funding 差门槛（百分点，2.0=2%）
	FreshnessTTL     time.Duration
}

func (d *PerpFundingDetector) Type() OppType { return TypePerpFunding }

// Scan 需要 funding Fact（通过 FactProvider 注入）+ QuoteBus snapshot。
// fundingFacts: canonical → venue → 年化百分点（正=多头付空头收，负=反向）。
func (d *PerpFundingDetector) Scan(_ context.Context, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate {
	var out []Candidate
	now := time.Now()
	var fundingFacts map[string]map[string]float64
	if fp != nil {
		fundingFacts = fp.FundingFacts()
	}
	for canonical, venues := range snapshot {
		// 收集所有 perp venue 的报价 + funding
		type perpLeg struct {
			q       bus.Quote
			funding float64
			ok      bool
		}
		var legs []perpLeg
		for _, q := range venues {
			if q.VenueType != "perp" {
				continue
			}
			if stale(q, now, d.FreshnessTTL) {
				continue
			}
			funding, ok := lookupFunding(fundingFacts, canonical, q.Venue)
			if !ok {
				continue
			}
			legs = append(legs, perpLeg{q: q, funding: funding, ok: true})
		}
		// 两两配对（i < j 避免重复 + 同所跳过）
		for i := 0; i < len(legs); i++ {
			for j := i + 1; j < len(legs); j++ {
				a, b := legs[i], legs[j]
				if a.q.Venue == b.q.Venue {
					continue // 同所不算跨所
				}
				// 净年化 = |fundingA - fundingB|（高 funding 端持空收、低 funding 端持多付）
				spread := a.funding - b.funding
				var buyLeg, sellLeg bus.Quote
				if spread > 0 {
					// A funding 高 → A 端持空（sell）、B 端持多（buy）
					buyLeg, sellLeg = b.q, a.q
				} else {
					// B funding 高 → B 端持空（sell）、A 端持多（buy）
					buyLeg, sellLeg = a.q, b.q
					spread = -spread
				}
				// gross = funding 差（年化百分点），evaluator 扣 marginRate 得 net
				if spread < d.MinNetAnnualized {
					continue
				}
				qt := a.q.Time
				if b.q.Time.After(qt) {
					qt = b.q.Time
				}
				out = append(out, Candidate{
					Type:      TypePerpFunding,
					Canonical: canonical,
					VenueBuy:  buyLeg.VenueKey(),
					VenueSell: sellLeg.VenueKey(),
					PriceBuy:  buyLeg.Ask,
					PriceSell: sellLeg.Bid,
					GrossBps:  spread,
					QuoteTime: qt.UnixMilli(),
				})
			}
		}
	}
	return out
}
