package dashboard

// service_graduation_pools.go D-508 S8：毕业授权 dry_run 腿→资金池映射下发（自 sizing 拆出）。

import (
	"math"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"

	"google.golang.org/protobuf/types/known/timestamppb"
)

// gradLegPoolsOf 池行组装（D-542：与 legPool 求值解耦——调用方先取池再渲染，
// source 透出 ws/rest 实拉与 snap 快照兜底，前端「实拉/快照」标签数据源）。
func gradLegPoolsOf(legKeys []string, pools []allocPool, oks []bool) []*v1.GradLegPoolRow {
	rows := make([]*v1.GradLegPoolRow, 0, len(legKeys))
	minEq := math.Inf(1)
	for i, vk := range legKeys {
		p := pools[i]
		row := &v1.GradLegPoolRow{VenueKey: vk, Found: oks[i]}
		if oks[i] {
			k := keydomain.Parse(p.key)
			row.PoolVenue, row.PoolVenueType = k.Venue, k.Type
			row.Equity, row.Currency = p.equity, p.currency
			row.Source = "snap"
			if p.live {
				row.Source = p.src // ws=私有流实拉 / rest=实拉校准
			}
			if !p.at.IsZero() {
				row.UpdatedAt = timestamppb.New(p.at)
			}
			if p.equity < minEq {
				minEq = p.equity
			}
		}
		rows = append(rows, row)
	}
	for i, p := range pools {
		if rows[i].Found && p.equity == minEq && len(legKeys) > 1 {
			rows[i].IsMin = true
		}
	}
	return rows
}
