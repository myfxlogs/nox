// earn_rail_binance.go —— binance rail 包壳（earn_rail.go 拆出保行数闸）。
// 原实现零行为变：真 HTTP 走既有 earnProducts/earnSubscribe/freeBalance/
// redeemFlexible/earnPositionsOf 方法体。
package dciexec

import (
	"context"
)

// binanceEarnRail 原径包壳（行为零变——真 HTTP 走既有 earnProducts/earnSubscribe/
// freeBalance/redeemFlexible 方法体）。
type binanceEarnRail struct{ e *Executor }

func (r *binanceEarnRail) Products(ctx context.Context, ccy string) ([]EarnProduct, error) {
	return r.e.earnProducts(ctx, ccy)
}
func (r *binanceEarnRail) Subscribe(ctx context.Context, productID string, amount float64) (string, error) {
	return r.e.earnSubscribe(ctx, productID, amount)
}
func (r *binanceEarnRail) Redeem(ctx context.Context, productID string, amount float64) (string, error) {
	return r.e.redeemFlexible(ctx, productID, amount)
}
func (r *binanceEarnRail) FreeBalance(ctx context.Context, ccy string) (float64, error) {
	return r.e.freeBalance(ctx, ccy)
}

// Positions binance 活期在册（locked 定期剔除——轮动只赎活期面）。
func (r *binanceEarnRail) Positions(ctx context.Context, ccy string) ([]EarnPosition, error) {
	eps, err := r.e.earnPositionsOf(ctx, ccy)
	if err != nil {
		return nil, err
	}
	out := eps[:0]
	for _, p := range eps {
		if !p.Locked {
			out = append(out, p)
		}
	}
	return out, nil
}
