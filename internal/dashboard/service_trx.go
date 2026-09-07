// D-211 TRX 质押监控 P1：rollup（SSOT §5）。ListTRXAccount RPC 已在 D-212 退役（统一流水卡替代）。
package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/bus"
)

// RollupTrxDailyIncome 日终 rollup（SSOT §5）：从 fin_trx_* 表汇总当日收入写入 position_daily_income(source='auto')。
// 快照窗口=D 日 [00:00, 23:55]；23:55→00:00(D+1) 的增量归 D+1 日。
// energy_trx = 当日 rental_income 之和（sun ÷ 1e6）；voting_trx = Σmax(0,Δallowance) 相邻快照正增量。
// price/pnl 快照复用手工语义（qbus TRXUSDT+陈旧守卫+空单浮亏）。
func (s *Service) RollupTrxDailyIncome(ctx context.Context, day time.Time) error {
	day = day.UTC().Truncate(24 * time.Hour)

	// energy_trx = 当日 rental_income 之和（sun → TRX）
	rentalSun, err := s.store.SumTRXTxRentalByDay(ctx, day)
	if err != nil {
		return fmt.Errorf("rollup energy: %w", err)
	}
	energyTrx := float64(rentalSun) / 1e6

	// voting_trx = Σmax(0, Δallowance) 相邻 5min 快照正增量（F2：剥离 claim 清零）
	accounts, err := s.store.ListTRXAccountByDay(ctx, day)
	if err != nil {
		return fmt.Errorf("rollup voting: %w", err)
	}
	var votingTrx float64
	for i := 1; i < len(accounts); i++ {
		delta := accounts[i].AllowanceSun - accounts[i-1].AllowanceSun
		if delta > 0 {
			votingTrx += float64(delta) / 1e6
		}
	}

	// price/pnl 快照（F5：复用手工语义——qbus TRXUSDT mid+陈旧守卫+空单浮亏）
	var price *float64
	snap := s.qbus.Snapshot(ctx, []string{"TRXUSDT"})
	if q, ok := bus.LatestQuote(snap["TRXUSDT"], s.freshTTL, time.Now()); ok {
		p := q.Mid()
		price = &p
	}
	var pnl *float64
	if price != nil {
		positions, err := s.store.ListPersonalPositions(ctx, "OPEN", 200, 0)
		if err != nil {
			return fmt.Errorf("rollup pnl: %w", err)
		}
		for i := range positions {
			p := &positions[i]
			if p.Kind == "short_hedge" && p.EntryPrice != nil && p.Qty != nil {
				v := (*p.EntryPrice - *price) * *p.Qty
				pnl = &v
				break
			}
		}
	}

	// D-253：interest_usd 并入 CMC overnight（当日 SumCMCOvernightByRange，治本累计低估 ~$6.5）
	dayStart := day
	dayEnd := day.Add(24 * time.Hour)
	overnight, err := s.store.SumCMCOvernightByRange(ctx, &dayStart, &dayEnd)
	if err != nil {
		return fmt.Errorf("rollup overnight: %w", err)
	}

	note := fmt.Sprintf("auto: energy=租金 %v TRX / voting=Σ正增量 %v TRX / overnight=%v USD", energyTrx, votingTrx, overnight)
	if err := s.store.AddDailyIncomeAuto(ctx, day, energyTrx, votingTrx, overnight, price, pnl, note); err != nil {
		return fmt.Errorf("rollup write: %w", err)
	}
	return nil
}
