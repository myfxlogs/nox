package dashboard

// service_hedge_structure_test.go —— GetHedgeStructure 锚：seed 四源行→复合
// 结构对拍（双腿/净敞口/carry/双账户+nil provider=DB 兜底径覆盖）。
import (
	"context"
	"testing"
	"time"

	"connectrpc.com/connect"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
)

func TestGetHedgeStructure_DBCompose(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// TRX 账户：质押 18070 TRX（tron_power=sun×1e6）+流动 50 TRX+待领 1.5 TRX
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "T1", VotesTotal: 18070, TronPower: 18_070_000_000, BalanceSun: 50_000_000,
		RewardNumSun: 1_500_000, EnergyDelegated: 8_000_000, BandwidthStaked: 2_000_000,
		NetLimit: 600_000, NetUsed: 100_000, FreeNetLimit: 400_000, FreeNetUsed: 10_000,
		EnergyLimit: 700_000, EnergyUsed: 50_000, FetchedAt: now,
	}); err != nil {
		t.Fatalf("trx acc: %v", err)
	}
	// CMC 空单 18100：entry=0.34，eval bid/ask=0.3495/0.3505，PnL=−18.1，margin%=0.25→lev=4
	if err := s.InsertCMCPositions(ctx, 38941616, []pgstore.CMCPosition{{
		AccountID: 38941616, InstrumentID: "X-AFQUL", Direction: "SELL",
		Quantity: 18100, EntryPrice: 0.34, UnrealisedPnL: -18.1,
		MarginAmount: 1538.5, MarginPercentage: 0.25,
		EvaluationBidPrice: 0.3495, EvaluationAskPrice: 0.3505, FetchedAt: now,
	}}); err != nil {
		t.Fatalf("cmc pos: %v", err)
	}
	// CMC 账户摘要：closeout=0.42、margin=4000、dist=19.7%
	if err := s.InsertCMCAccountSummary(ctx, pgstore.CMCAccountSummary{
		AccountID: 38941616, TotalAccountValue: 12000, TotalCashBalance: 11000,
		TotalUnrealisedPnL: -18.1, TotalEquityUsedInMargin: 4000,
		CloseoutPrice: 0.42, DistanceToCloseoutPct: 19.7, FetchedAt: now,
	}); err != nil {
		t.Fatalf("cmc sum: %v", err)
	}
	// 隔夜息两行（近 7 日窗内）：−0.50/−0.45
	for _, amt := range []float64{-0.50, -0.45} {
		if _, err := s.Pool().Exec(ctx, `INSERT INTO fin_cmc_history
			(account_id, transaction_id, transaction_type, amount, currency, transaction_time)
			VALUES ($1,$2,'OVERNIGHT_SETTLEMENT',$3,'USD',$4)`,
			38941616, "ov-"+time.Now().String()+string(rune(int(-amt*100))), amt, now.Add(-24*time.Hour)); err != nil {
			t.Fatalf("overnight seed: %v", err)
		}
	}
	// 日收益两行：energy 10/voting 5/price 0.35/interest −0.5
	for i, d := range []time.Time{now.AddDate(0, 0, -2), now.AddDate(0, 0, -1)} {
		if _, err := s.Pool().Exec(ctx, `INSERT INTO position_daily_income
			(day, energy_trx, voting_trx, interest_usd, price, source)
			VALUES ($1,$2,$3,$4,$5,'auto')`, d, 10.0+float64(i), 5.0, -0.5, 0.35); err != nil {
			t.Fatalf("income seed: %v", err)
		}
	}

	svc := &Service{store: s}
	resp, err := svc.GetHedgeStructure(ctx, connect.NewRequest(&v1.GetHedgeStructureRequest{}))
	if err != nil {
		t.Fatalf("rpc: %v", err)
	}
	st := resp.Msg.Structure
	if st == nil {
		t.Fatal("structure nil")
	}
	// 双腿
	if st.LegSpot.QtyStaked != 18070 || st.LegSpot.QtyLiquid != 50 {
		t.Fatalf("spot leg: %+v", st.LegSpot)
	}
	if st.LegSpot.PnlVsBase != 18.1 { // 镜像=−(−18.1)
		t.Fatalf("spot mirror pnl: %v", st.LegSpot.PnlVsBase)
	}
	if st.LegShort.Qty != 18100 || st.LegShort.PnlVsBase != -18.1 || st.LegShort.Lev != 4 {
		t.Fatalf("short leg: %+v", st.LegShort)
	}
	if st.LegShort.Liq != 0.42 || st.LegShort.DistPct != 19.7 {
		t.Fatalf("short liq/dist: %+v", st.LegShort)
	}
	// 净敞口=(50+18.07−18100)×mid——qty 大净敞为负
	if st.NetExposureTrx != 50+18070-18100 {
		t.Fatalf("net exposure trx: %v", st.NetExposureTrx)
	}
	// baseline/ask/mid：DB 兜底径（mid=evaluation_bid）
	if st.BaselinePrice != 0.34 || st.Ask != 0.3505 || st.Mid != 0.3495 {
		t.Fatalf("price fields: base=%v ask=%v mid=%v", st.BaselinePrice, st.Ask, st.Mid)
	}
	// carry：7d 窗 energy=20/voting=10、overnight=−0.95
	var wantE, wantV float64
	if err := s.Pool().QueryRow(ctx, `SELECT COALESCE(SUM(energy_trx),0), COALESCE(SUM(voting_trx),0)
		FROM position_daily_income WHERE day >= $1::date`, now.Add(-7*24*time.Hour).Format("2006-01-02")).Scan(&wantE, &wantV); err != nil {
		t.Fatalf("ev want: %v", err)
	}
	if st.Carry.TrxRental_7DTrx != wantE || st.Carry.TrxVote_7DTrx != wantV {
		t.Fatalf("carry 7d: got %v/%v want %v/%v", st.Carry.TrxRental_7DTrx, st.Carry.TrxVote_7DTrx, wantE, wantV)
	}
	// 隔夜息=SQL 直读对拍（测试库 fixture 可能叠加——端到端验查询径非钉定值）
	var wantOv float64
	if err := s.Pool().QueryRow(ctx, `SELECT COALESCE(SUM(amount),0) FROM fin_cmc_history
		WHERE transaction_type='OVERNIGHT_SETTLEMENT' AND transaction_time >= $1`,
		now.Add(-7*24*time.Hour)).Scan(&wantOv); err != nil {
		t.Fatalf("ov want: %v", err)
	}
	if st.Carry.CmcOvernight_7DUsd != wantOv {
		t.Fatalf("carry overnight: got %v want %v", st.Carry.CmcOvernight_7DUsd, wantOv)
	}
	// 双账户
	if st.Accts.TrxBalance != 50 || st.Accts.TrxAllowance != 1.5 || st.Accts.CmcCash != 11000 || st.Accts.CmcEquity != 12000 {
		t.Fatalf("accts: %+v", st.Accts)
	}
	if st.Accts.BandwidthStatus == "" {
		t.Fatalf("bandwidth status empty")
	}
	// 平台口径：margin=4000→closeout=2000/warning=3200；source 字
	if st.CloseoutLevelUsd != 2000 || st.WarningLevelUsd != 3200 {
		t.Fatalf("closeout/warning levels: %v %v", st.CloseoutLevelUsd, st.WarningLevelUsd)
	}
	if st.CloseoutSource != "platform_rule_50" {
		t.Fatalf("closeout source: %v", st.CloseoutSource)
	}
}
