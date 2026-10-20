// service_capital_coverage_pools.go —— D-359 池行构造（TRX；CMC 池随族件剔除；自主文件拆出控行数 ≤300）。
// 口径 SSOT 与主文件头注同源；digest capital_coverage*.go 同口径双实现。
package dashboard

import (
	"context"
	"fmt"
	"nox/internal/data/bus"
	"nox/internal/store/pgstore"

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
	// D-629 业绩口径剔 TRX（业主令）：质押/投票/能量=本金自带链上回报，非系统
	// 发现/评估/执行产出→业绩外行（RealMoney/Productive=false 不入总资金分母与
	// 在产分子；APR 仅落 note 展示不入加权年化）；行保留可见（钱在哪仍要看得见）。
	r := covRow("TRX 质押+投票+能量")
	r.BalanceCcy = "USD" // D-130 域3：TRX 行=折算 USD 值
	r.Status = "业绩外·自营盘"
	r.Productive = false
	r.RealMoney = false
	voteAPR, hasVote := 0.0, false
	if va, err := s.store.LastTRXVoteApr(ctx); err == nil {
		voteAPR, hasVote = va.CurrentRate, true
	}
	energyAPR, sampleDays, err := s.TrxEnergyRentalAPR(ctx, 30)
	// D-258 语义：样本<30d/零在押 → aprPct=0 不猜；sampleDays>=30 即有效读数
	energyOK := err == nil && sampleDays >= 30
	note := "业绩外·自营盘（D-629：不入总资金/在产/加权年化）"
	if hasVote {
		note += fmt.Sprintf("；投票 %.2f%%", voteAPR)
	}
	if energyOK {
		note += fmt.Sprintf("+能量租赁 %.2f%%", energyAPR)
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
	// D-633 现态切 qbus（binance:spot）
	q, ok := quoteOrNil(s.qbus, "TRXUSDT", "binance:spot")
	if !ok {
		var t pgstore.TickRow
		var tok bool
		var terr error
		if t, tok, terr = s.store.LatestTick(ctx, "binance", "spot", "TRXUSDT"); !tok || terr != nil {
			t, tok, terr = s.store.LatestTick(ctx, "binance", "perp", "TRXUSDT")
		}
		if tok && terr == nil {
			q, ok = bus.Quote{Bid: t.Bid, Ask: t.Ask, Time: t.Ts}, true
		}
	}
	if ok && q.Bid > 0 && q.Ask > 0 {
		return (q.Bid + q.Ask) / 2, true
	}
	return 0, false
}
