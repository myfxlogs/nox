package dashboard

// service_graduation_pools.go D-508 S8：毕业授权 dry_run 腿→资金池映射下发（自 sizing 拆出）。

import (
	"math"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// gradLegPools 双腿资金池映射组装（D-508 S8）：逐腿 allocPoolOf 终值 + 小池标记。
func gradLegPools(snaps []pgstore.VenueAccountLatest, legKeys []string) []*v1.GradLegPoolRow {
	rows := make([]*v1.GradLegPoolRow, 0, len(legKeys))
	pools := make([]allocPool, len(legKeys))
	minEq := math.Inf(1)
	for i, vk := range legKeys {
		p, ok := allocPoolOf(snaps, vk)
		row := &v1.GradLegPoolRow{VenueKey: vk, Found: ok}
		if ok {
			k := keydomain.Parse(p.key)
			row.PoolVenue, row.PoolVenueType = k.Venue, k.Type
			row.Equity, row.Currency = p.equity, p.currency
			if !p.at.IsZero() {
				row.UpdatedAt = timestamppb.New(p.at)
			}
			if p.equity < minEq {
				minEq = p.equity
			}
		}
		pools[i] = p
		rows = append(rows, row)
	}
	for i, p := range pools {
		if rows[i].Found && p.equity == minEq && len(legKeys) > 1 {
			rows[i].IsMin = true
		}
	}
	return rows
}
