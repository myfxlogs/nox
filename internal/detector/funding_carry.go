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
	BaselinePct      float64 // 同所基准费率年化（%，D-084：同所组合扣基准后过门槛；≤0 关闭扣除）
	FreshnessTTL     time.Duration
}

func (d *FundingHedgeDetector) Type() OppType { return TypeFundingHedge }

// Scan 需要 funding Fact（通过 FactMap 注入）+ QuoteBus snapshot。
// FactMap: canonical → venue → funding Fact（已年化）。
func (d *FundingHedgeDetector) Scan(_ context.Context, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate {
	var out []Candidate
	now := time.Now()
	var fundingFacts map[string]map[string]float64
	var realMap map[string]*bool
	if fp != nil {
		fundingFacts = fp.FundingFacts()
		realMap = fp.IsRealMap()
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
		// D-178：is_real 配对检查（fail-open，gate 层兜底）。
		if !isRealPair(realMap, spot.VenueKey(), perp.VenueKey()) {
			continue
		}
		funding, ok := lookupFunding(fundingFacts, canonical, perp.Venue)
		if !ok {
			continue
		}
		netAnnualized := funding
		// D-084 修正：spot+perp 结构无差分（收益恒=perp 腿 venue 的 funding），跨所同所统一扣基准；
		// 跨所版保留发射（库存币顺手套保场景：币在 A 所、B 所尖峰时跨所对冲），但过同一超额尺子。
		// 入库行 GrossBps 保持原值（显示完整毛收益），扣基准只用于门槛判定（最简，显示层零改动）。
		// BaselinePct≤0 关闭扣除（兼容开关）。
		if d.BaselinePct > 0 {
			if netAnnualized-d.BaselinePct < d.MinNetAnnualized {
				continue
			}
		} else if netAnnualized < d.MinNetAnnualized {
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

// D-187：CarryDetector 已删除——CARRY 候选由 autotrade.discoverSwapCombos 统一产出
// （netSwap 换算年化 ≥ CarryMinNetAnnualized → strategy="CARRY"）。CARRY 策略类型保留
// （position.Type="CARRY"），evaluator/netStoreGate CARRY 分支保留不删（向后兼容）。
// lookupSwap/SwapFactsToMap 保留供未来恢复 CARRY detector 或其他消费方使用。

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
