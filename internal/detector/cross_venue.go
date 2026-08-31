package detector

import (
	"context"
	"time"

	"arb/internal/bus"
)

// CrossVenueDetector 检测同一 canonical 在不同 venue 的 bid/ask 价差（§5.1）。
type CrossVenueDetector struct {
	CexMinGrossBps float64       // CEX(spot/perp) 跨所 gross 门槛（bps）
	CfdMinGrossBps float64       // CFD(mt4/mt5) 跨所 gross 观察门槛（bps）
	FreshnessTTL   time.Duration // §5.0 陈旧报价排除
}

func (d *CrossVenueDetector) Type() OppType { return TypeCrossVenue }

func (d *CrossVenueDetector) Scan(_ context.Context, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate {
	var out []Candidate
	now := time.Now()
	var modes map[string]map[string]int64
	var realMap map[string]*bool
	if fp != nil {
		modes = fp.TradeModes()
		realMap = fp.IsRealMap()
	}
	for canonical, venues := range snapshot {
		if len(venues) < 2 {
			continue
		}
		vkeys := sortedKeys(venues)
		for i := range len(vkeys) {
			for j := i + 1; j < len(vkeys); j++ {
				a, b := venues[vkeys[i]], venues[vkeys[j]]
				if stale(a, now, d.FreshnessTTL) || stale(b, now, d.FreshnessTTL) {
					continue
				}
				minBps := crossMinBpsFor([]string{a.VenueType, b.VenueType}, d.CexMinGrossBps, d.CfdMinGrossBps)
				// 可交易性腿过滤（D-060 F4）：买腿须可开多、卖腿须可开空；未知放行。
				if !legTradable(modes, canonical, a.VenueKey(), true) || !legTradable(modes, canonical, b.VenueKey(), false) {
					continue
				}
				// D-178：is_real 配对检查（fail-open，对称覆盖正向+反向）。
				if !isRealPair(realMap, a.Venue, b.Venue) {
					continue
				}
				// 买 A（按 A.ask）卖 B（按 B.bid）：利润 = B.bid - A.ask（D-030 修正符号）
				if c := tryCross(canonical, &a, &b, minBps); c != nil {
					out = append(out, *c)
				}
				// 反向：买 B 卖 A
				if !legTradable(modes, canonical, b.VenueKey(), true) || !legTradable(modes, canonical, a.VenueKey(), false) {
					continue
				}
				if c := tryCross(canonical, &b, &a, minBps); c != nil {
					out = append(out, *c)
				}
			}
		}
	}
	return out
}

func tryCross(canonical string, buy, sell *bus.Quote, minBps float64) *Candidate {
	spread := sell.Bid - buy.Ask
	if spread <= 0 {
		return nil
	}
	mid := buy.Mid()
	if mid <= 0 {
		return nil
	}
	grossBps := spread / mid * 10000
	if grossBps < minBps {
		return nil
	}
	qt := buy.Time
	if sell.Time.After(qt) {
		qt = sell.Time
	}
	return &Candidate{
		Type:      TypeCrossVenue,
		Canonical: canonical,
		VenueBuy:  buy.VenueKey(),
		VenueSell: sell.VenueKey(),
		PriceBuy:  buy.Ask,
		PriceSell: sell.Bid,
		GrossBps:  grossBps,
		QuoteTime: qt.UnixMilli(),
	}
}

// crossMinBpsFor 按双腿 venue 类型取 cross 毛门槛：双腿均 CEX(spot/perp)→cex；均 CFD(mt4/mt5)→cfd；混合→max(保守)。
// D-051 WP1：CFD 门槛 2.0 匹配市场量级，CEX 保持 10.0；展示仍靠评估器 net>0（trap 抑制）。
func crossMinBpsFor(venueTypes []string, cex, cfd float64) float64 {
	var hasCEX, hasCFD bool
	for _, vt := range venueTypes {
		switch vt {
		case "spot", "perp":
			hasCEX = true
		case "mt4", "mt5":
			hasCFD = true
		}
	}
	switch {
	case hasCFD && !hasCEX:
		return cfd
	case hasCEX && !hasCFD:
		return cex
	default: // 两者都有 / 都无 / 未知类型 → max（保守）
		if cex > cfd {
			return cex
		}
		return cfd
	}
}

func stale(q bus.Quote, now time.Time, ttl time.Duration) bool {
	if ttl <= 0 {
		return false
	}
	return now.Sub(q.Time) > ttl
}

func sortedKeys(m map[string]bus.Quote) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sortStrings(keys)
	return keys
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j-1] > s[j]; j-- {
			s[j-1], s[j] = s[j], s[j-1]
		}
	}
}
