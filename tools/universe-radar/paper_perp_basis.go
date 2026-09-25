// paper_perp_basis.go D-474b S1：perp×perp 两腿 mark 价差 basis 采集——
// ticks 表 venue_type='perp' 新鲜 mid；任一腿缺 → nil 诚实缺（禁常数兜底）。
// 从 paper_perp.go 拆出保 300 行门禁。
package main

import (
	"context"
	"time"

	"arb/internal/store/pgstore"
)

// perpTickBasis D-474b S1：两腿 mark（mid）价差 bps=(px_sell−px_buy)/mid×1e4，
// px_sell=short venue mid、px_buy=long venue mid、mid=两腿均值。tick 源=ticks 表
// （venue=cex 裸名、venue_type='perp'）；任一腿无新鲜 tick → nil（缺账诚实缺，禁常数兜底）。
func perpTickBasis(ctx context.Context, st *pgstore.Store, shortVenue, shortSym, longVenue, longSym string, now time.Time) *float64 {
	ps, okS := perpLatestTickMid(ctx, st, shortVenue, shortSym, now)
	pl, okL := perpLatestTickMid(ctx, st, longVenue, longSym, now)
	if !okS || !okL {
		return nil
	}
	mid := (ps + pl) / 2
	if mid <= 0 {
		return nil
	}
	b := (ps - pl) / mid * 1e4
	return &b
}

// perpLatestTickMid venue|symbol 最新 perp tick 的 mid 价（窗口内倒序取首条 venue_type='perp'）。
func perpLatestTickMid(ctx context.Context, st *pgstore.Store, venue, symbol string, now time.Time) (float64, bool) {
	ticks, err := st.TicksWindow(ctx, venue, symbol, now.Add(-perpTickMaxAge))
	if err != nil {
		return 0, false
	}
	for i := len(ticks) - 1; i >= 0; i-- {
		if t := ticks[i]; t.VenueType == "perp" && t.Bid > 0 && t.Ask >= t.Bid {
			return (t.Bid + t.Ask) / 2, true
		}
	}
	return 0, false
}
