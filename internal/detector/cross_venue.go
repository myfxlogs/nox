package detector

import (
	"context"
	"time"

	"arb/internal/bus"
)

// CrossVenueDetector 检测同一 canonical 在不同 venue 的 bid/ask 价差（§5.1）。
type CrossVenueDetector struct {
	MinGrossBps         float64
	FreshnessTTL        time.Duration // §5.0 陈旧报价排除
}

func (d *CrossVenueDetector) Type() OppType { return TypeCrossVenue }

func (d *CrossVenueDetector) Scan(_ context.Context, snapshot map[string]map[string]bus.Quote, _ FactProvider) []Candidate {
	var out []Candidate
	now := time.Now()
	for canonical, venues := range snapshot {
		if len(venues) < 2 {
			continue
		}
		vkeys := sortedKeys(venues)
		for i := 0; i < len(vkeys); i++ {
			for j := i + 1; j < len(vkeys); j++ {
				a, b := venues[vkeys[i]], venues[vkeys[j]]
				if stale(a, now, d.FreshnessTTL) || stale(b, now, d.FreshnessTTL) {
					continue
				}
				// 买 A（按 A.ask）卖 B（按 B.bid）：利润 = B.bid - A.ask（D-030 修正符号）
				if c := tryCross(canonical, &a, &b, d.MinGrossBps, now); c != nil {
					out = append(out, *c)
				}
				// 反向：买 B 卖 A
				if c := tryCross(canonical, &b, &a, d.MinGrossBps, now); c != nil {
					out = append(out, *c)
				}
			}
		}
	}
	return out
}

func tryCross(canonical string, buy, sell *bus.Quote, minBps float64, now time.Time) *Candidate {
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
