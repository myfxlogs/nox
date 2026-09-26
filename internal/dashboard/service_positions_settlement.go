package dashboard

import (
	"context"
	"fmt"

	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
)

// enrichSettlements D-106：批量 join position_settlements 填充 Position 的
// settled_realized_usd(19) / settled_notional_usd(20) / hold_days(21)。
//
// 填充规则（提示词 §2）：
//   - 结算行存在 → settled_realized_usd=realized_usd, settled_notional_usd=notional_usd（NULL 留缺省）,
//     hold_days=结算行 hold_days（D-087 已算权威值）。
//   - 结算行不存在（未结算/活跃行）→ 三字段留缺省（proto optional 不设）；hold_days 不回退计算
//     （活跃行 hold_days 由前端 opened_at 实时算更准；CLOSED 无结算行=异常留缺省，诚实口径）。
//
// posIDs 空 → 直接返回（不查库）。
func enrichSettlements(ctx context.Context, store settlementQuerier, pbPositions []*v1.Position, posIDs []int64) error {
	if len(posIDs) == 0 {
		return nil
	}
	settlements, err := store.ListSettlementsByPosIDs(ctx, posIDs)
	if err != nil {
		return fmt.Errorf("list settlements: %w", err)
	}
	for _, pb := range pbPositions {
		sr, ok := settlements[pb.Id]
		if !ok {
			continue // 无结算行 → 留缺省
		}
		pb.SettledRealizedUsd = &sr.RealizedUsd
		if sr.NotionalUsd != nil {
			pb.SettledNotionalUsd = sr.NotionalUsd
		}
		pb.HoldDays = &sr.HoldDays
	}
	return nil
}

// settlementQuerier ListSettlementsByPosIDs 接口提取（测试可注入 fake，生产用 *pgstore.Store）。
type settlementQuerier interface {
	ListSettlementsByPosIDs(ctx context.Context, posIDs []int64) (map[int64]*pgstore.SettlementRow, error)
}
