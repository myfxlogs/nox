// wire_depth_okx.go —— books5 顶档深度帧→fin_cex_depth 同形写（REST 拍降为
// 断线兜底——ws 主供；CexDepthRowLite→CexDepthRow 拼装件）。
package main

import (
	"context"

	"nox/internal/data/collect/exchange"
	"nox/internal/store/pgstore"
)

func okxDepthEmit(st *pgstore.Store) func(context.Context, []exchange.CexDepthRowLite) error {
	return func(ctx context.Context, rows []exchange.CexDepthRowLite) error {
		out := make([]pgstore.CexDepthRow, 0, len(rows))
		for _, r := range rows {
			out = append(out, pgstore.CexDepthRow{
				Venue: r.Venue, Symbol: r.Symbol, Ts: r.Ts,
				Bid1USD: r.Bid1USD, Ask1USD: r.Ask1USD,
				Bid1Qty: r.Bid1Qty, Ask1Qty: r.Ask1Qty,
				// 名义深度=mid×min(bidQty,askQty)（契约有据 MidDepthUSD 深度列位）。
				MidDepthUSD: (r.Bid1USD + r.Ask1USD) / 2 * minF(r.Bid1Qty, r.Ask1Qty),
			})
		}
		return st.InsertCexDepth(ctx, out)
	}
}

// minF 小额件——两 qty 取小（mid×min 名义深度件参）。
func minF(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
