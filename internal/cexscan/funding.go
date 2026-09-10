// D-271 S3 Funding 扫描：读 pgstore funding facts（不调交易所 API）。
package cexscan

import (
	"context"
	"fmt"
	"time"

	"arb/internal/store/pgstore"
)

// FundingEntry funding rate 条目（从 fact.Fact 映射）。
type FundingEntry struct {
	Venue  string
	Symbol string
	Rate   float64 // 小数年化（fact.Value）
	Ts     time.Time
}

// ScanFundingRates 调 store.LatestFacts("funding")，转 fact.Fact → FundingEntry。
func ScanFundingRates(ctx context.Context, store *pgstore.Store) ([]FundingEntry, error) {
	facts, err := store.LatestFacts(ctx, "funding", "", "")
	if err != nil {
		return nil, fmt.Errorf("scan funding rates: %w", err)
	}
	out := make([]FundingEntry, 0, len(facts))
	for _, f := range facts {
		out = append(out, FundingEntry{
			Venue:  f.Venue,
			Symbol: f.Symbol,
			Rate:   f.Value,
			Ts:     f.Ts,
		})
	}
	return out, nil
}
