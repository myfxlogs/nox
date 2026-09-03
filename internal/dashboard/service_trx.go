// D-211 TRX 质押监控 P1：rollup + ListTRXAccount RPC handler（SSOT §5/§6）。
package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/bus"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
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

	note := fmt.Sprintf("auto: energy=租金 %v TRX / voting=Σ正增量 %v TRX", energyTrx, votingTrx)
	if err := s.store.AddDailyIncomeAuto(ctx, day, energyTrx, votingTrx, 0, price, pnl, note); err != nil {
		return fmt.Errorf("rollup write: %w", err)
	}
	return nil
}

// ListTRXAccount 返回 TRX 账户摘要 + 最近 50 条流水（D-211：与 CMC 流水卡对称）。
// 摘要空表 → summary=nil（前端显示「TRX 数据未就绪」）；流水空表 → txs 空数组。
func (s *Service) ListTRXAccount(ctx context.Context, req *connect.Request[v1.ListTRXAccountRequest]) (*connect.Response[v1.ListTRXAccountResponse], error) {
	resp := &v1.ListTRXAccountResponse{}

	// 摘要（空表容忍——TRX 采集器未跑或表空时 summary=nil）
	acc, err := s.store.LastTRXAccount(ctx)
	if err == nil {
		srAddr := ""
		if acc.SRAddress != nil {
			srAddr = *acc.SRAddress
		}
		resp.Summary = &v1.TRXAccountInfo{
			BalanceTrx:        float64(acc.BalanceSun) / 1e6,
			AllowanceTrx:      float64(acc.AllowanceSun) / 1e6,
			EnergyDelegatedTrx: float64(acc.EnergyDelegated) / 1e6,
			VotesTotal:        acc.VotesTotal,
			SrAddress:         srAddr,
			FetchedAt:         timestamppb.New(acc.FetchedAt),
		}
	}

	// 流水（最近 50 条倒序）
	txs, err := s.store.ListTRXTxs(ctx, 50)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list trx txs: %w", err))
	}
	for _, t := range txs {
		item := &v1.TRXTxItem{
			TxId:      t.TxID,
			Ts:        timestamppb.New(t.Ts),
			Kind:      t.Kind,
			AmountTrx: float64(t.AmountSun) / 1e6,
		}
		if t.Counterparty != nil {
			item.Counterparty = *t.Counterparty
		}
		resp.Txs = append(resp.Txs, item)
	}

	return connect.NewResponse(resp), nil
}
