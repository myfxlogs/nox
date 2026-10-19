// cost_model_write.go —— D-625 P0 续件：fill 实收→fin_cost_model 写径
// （venue×结构×时段桶 UPSERT 累计；成本实测=回测准入的自我校准原料）。
package pgstore

import (
	"context"
	"fmt"
)

// RecordCostModel 单笔 fill 实收累计写（bucket_hour=-1=全天桶先粗后细）。
// fills+1；fee/drift/latency/funding 双边累计——均值由消费面按 fills 折算。
func (s *Store) RecordCostModel(ctx context.Context, venue, venueType, structure string,
	feeUSD, driftBps float64, latencyMs int64, fundingExp, fundingAct float64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_cost_model (venue, venue_type, structure, bucket_hour,
			fills, fee_usd, drift_bps, latency_ms, funding_exp, funding_act, updated_at)
		VALUES ($1,$2,$3,-1, 1,$4,$5,$6,$7,$8, now())
		ON CONFLICT (venue, venue_type, structure, bucket_hour) DO UPDATE SET
			fills       = fin_cost_model.fills + 1,
			fee_usd     = fin_cost_model.fee_usd + $4,
			drift_bps   = fin_cost_model.drift_bps + $5,
			latency_ms  = fin_cost_model.latency_ms + $6,
			funding_exp = fin_cost_model.funding_exp + $7,
			funding_act = fin_cost_model.funding_act + $8,
			updated_at  = now()`,
		venue, venueType, structure, feeUSD, driftBps, latencyMs, fundingExp, fundingAct)
	if err != nil {
		return fmt.Errorf("record cost model: %w", err)
	}
	return nil
}
