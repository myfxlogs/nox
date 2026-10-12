// WitnessCollector（24h）——自 collector.go 拆出（D-309 纯移动，零行为变更）。
package trx

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

// ---------- WitnessCollector（24h） ----------

// WitnessCollector 采集见证人快照：listwitnesses 过滤 isJobs 行。
type WitnessCollector struct {
	address string
	store   *pgstore.Store
	base    string
	apiKey  func() string
}

// NewWitnessCollector 创建见证人采集器。address 用于取投票 SR 地址。
func NewWitnessCollector(address string, store *pgstore.Store, base string, apiKey func() string) *WitnessCollector {
	if base == "" {
		base = DefaultBase
	}
	return &WitnessCollector{address: address, store: store, base: base, apiKey: apiKey}
}

// Kind 返回采集器标识。
func (c *WitnessCollector) Kind() string { return "trx_witness" }

// Poll 采集 listwitnesses 并只存 isJobs 行（在任 SR）。
func (c *WitnessCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	body, err := tronPost(ctx, c.base, c.apiKey(), "/wallet/getaccount",
		fmt.Sprintf(`{"address":"%s","visible":true}`, c.address))
	if err != nil {
		return nil, fmt.Errorf("trx witness getaccount: %w", err)
	}
	var acc accountJSON
	if err := json.Unmarshal(body, &acc); err != nil {
		return nil, fmt.Errorf("trx witness getaccount unmarshal: %w", err)
	}
	if len(acc.Votes) == 0 {
		return nil, nil // 无投票
	}

	body2, err := tronPost(ctx, c.base, c.apiKey(), "/wallet/listwitnesses", "{}")
	if err != nil {
		return nil, fmt.Errorf("trx listwitnesses: %w", err)
	}
	var resp witnessResponseJSON
	if err := json.Unmarshal(body2, &resp); err != nil {
		return nil, fmt.Errorf("trx listwitnesses unmarshal: %w", err)
	}
	now := time.Now()
	for _, w := range resp.Witnesses {
		if !w.IsJobs {
			continue // 只存在任 SR
		}
		var voteCount *int64
		if w.VoteCount > 0 {
			v := w.VoteCount
			voteCount = &v
		}
		var url *string
		if w.URL != "" {
			u := w.URL
			url = &u
		}
		if err := c.store.InsertTRXWitness(ctx, pgstore.TRXWitness{
			SRAddress: w.Address, URL: url, VoteCount: voteCount, FetchedAt: now,
		}); err != nil {
			return nil, fmt.Errorf("trx witness insert: %w", err)
		}
	}
	return nil, nil
}
