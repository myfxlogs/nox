// service_capital_coverage_pools.go —— D-359 池行构造（TRX；CMC 池随族件剔除；自主文件拆出控行数 ≤300）。
// 口径 SSOT 与主文件头注同源；digest capital_coverage*.go 同口径双实现。
package dashboard

import (
	"context"
	"fmt"

	v1 "nox/internal/gen/dashboardv1"
)

// trxPoolRow ①TRX：余额=(balance_sun+tron_power)/1e6×USD 价（D-356-R1 口径锚）；
// 年化=LastTRXVoteApr.CurrentRate+TrxEnergyRentalAPR(30)（D-258 口径复用零复刻）；
// 价 fallback=ticks TRXUSDT mid→无价「—」+数量入 note。
func trxPoolRow(ctx context.Context, s *Service) *v1.CapitalCoverageRow {
	acct, err := s.store.LastTRXAccount(ctx)
	if err != nil {
		return nil
	}
	r := covRow("TRX 质押+投票+能量")
	r.BalanceCcy = "USD" // D-130 域3：TRX 行=折算 USD 值
	r.Status = "在赚"
	r.Productive = true
	r.RealMoney = true
	voteAPR, hasVote := 0.0, false
	if va, err := s.store.LastTRXVoteApr(ctx); err == nil {
		voteAPR, hasVote = va.CurrentRate, true
		r.AprPct = voteAPR
		r.HasApr = true
	}
	energyAPR, sampleDays, err := s.TrxEnergyRentalAPR(ctx, 30)
	// D-258 语义：样本<30d/零在押 → aprPct=0 不猜；sampleDays>=30 即有效读数
	energyOK := err == nil && sampleDays >= 30
	note := ""
	if hasVote {
		note = fmt.Sprintf("投票 %.2f%%", voteAPR)
	}
	if energyOK {
		note += fmt.Sprintf("+能量租赁 %.2f%%", energyAPR)
		if hasVote {
			r.AprPct += energyAPR
		} else {
			r.AprPct = energyAPR
			r.HasApr = true
		}
	}
	balTRX := float64(acct.BalanceSun+acct.TronPower) / 1e6
	if price, ok := trxUSDPrice(ctx, s); ok {
		r.BalanceUsd = balTRX * price
		r.HasBalance = true
		r.Note = fmt.Sprintf("%s；余额=可用 %.0f+投票权 %.0f TRX", note,
			float64(acct.BalanceSun)/1e6, float64(acct.TronPower)/1e6)
	} else {
		r.Note = fmt.Sprintf("%s；TRX 数量 %.0f（可用 %.0f+投票权 %.0f，无 USD 价，不猜）", note,
			balTRX, float64(acct.BalanceSun)/1e6, float64(acct.TronPower)/1e6)
	}
	return r
}

// trxUSDPrice TRX USD 价（D-359：TRXUSD 价锚=binance 现货腿报价）。
func trxUSDPrice(ctx context.Context, s *Service) (float64, bool) {
	return s.store.LatestTickMid(ctx, "binance", "TRXUSDT")
}
