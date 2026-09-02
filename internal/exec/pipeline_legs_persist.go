package exec

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/store/pgstore"
)

// persistOpenLegs D-195-B：OPEN leg 写入循环（从 Pipeline.Execute 提取，避免 pipeline.go 超 300 行）。
// D-108 A1 fail-closed：腿写库失败 → error+critical 告警，孤儿不可留。
// D-195-B §4.3：client_order_id 确定性编码 + attempt 字段填充。
func (p *Pipeline) persistOpenLegs(ctx context.Context, requestID string, posID int64, legs []LegSpec, results []LegResult) error {
	for i, r := range results {
		clientOrderID := generateClientOrderID(requestID, posID, int16(i), 1) // attempt_no=1 首次
		now := time.Now()
		var uncertainAt *time.Time
		if r.Status == "UNKNOWN" {
			uncertainAt = &now
		}
		filledQty := r.Qty
		avgPrice := r.Price
		if err := p.store.CreatePositionLeg(ctx, &pgstore.PositionLeg{
			PositionID:    posID,
			LegNo:         int16(i),
			Phase:         "OPEN",
			AttemptNo:     1,
			Venue:         legs[i].VenueKey,
			Symbol:        legs[i].VenueSymbol,
			Side:          legs[i].Side,
			Qty:           legs[i].Qty,
			PriceExpected: legs[i].PriceExpected,
			PriceActual:   &r.Price,
			OrderID:       r.OrderID,
			Status:        r.Status,
			ClientOrderID: clientOrderID,
			RequestedQty:  &legs[i].Qty,
			FilledQty:     &filledQty,
			AvgFillPrice:  &avgPrice,
			SubmittedAt:   &now,
			UncertainAt:   uncertainAt,
		}); err != nil {
			msg := fmt.Sprintf("leg persist failed position=%d leg=%d venue=%s: %v", posID, i, legs[i].VenueKey, err)
			log.Printf("[exec] %s", msg)
			_ = p.store.InsertAlert(ctx, nil, "critical", msg)
			return fmt.Errorf("leg persist: %w", err)
		}
	}
	return nil
}
