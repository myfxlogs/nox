// D-211 S3 测试：RollupTrxDailyIncome（F2 Σ正增量/F5 price+pnl 快照）。
// ListTRXAccount RPC 测试已在 D-212 退役时删除（统一流水卡替代）。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// trxTestStore 创建测试 store（ARB_TEST_PG_SERVICE_DSN，TRUNCATE 全库）。
// D-212 CI 红根因修复：改为包级单例 pool（serviceTestStoreSingleInit），避免每测试 New 独立 pool
// 累积连接耗尽 pg 服务端 max_connections → Acquire 卡住 → 30s 超时 flaky。
func trxTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	// TRUNCATE 相关表（数据隔离）。
	for _, tbl := range []string{"fin_trx_account", "fin_trx_txs", "fin_trx_witness", "fin_trx_vote_apr", "fin_trx_cycle_vi", "position_daily_income", "personal_positions"} {
		if _, err := s.Pool().Exec(ctx, "TRUNCATE TABLE "+tbl+" RESTART IDENTITY CASCADE"); err != nil {
			t.Fatalf("TRUNCATE %s: %v", tbl, err)
		}
	}
	return s
}

// TestRollupTrxDailyIncome_F2VotingPositiveDelta F2 锚：voting=Σmax(0,Δallowance) 正增量。
// 构造 3 条账户快照：allowance 100→150→120（正增量 50+0=50，claim 清零不累入）。
func TestRollupTrxDailyIncome_F2VotingPositiveDelta(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	// 3 条账户快照（allowance 100→150→120 sun）
	for i, allowance := range []int64{100, 150, 120} {
		a := pgstore.TRXAccount{
			Address:    "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555",
			VotesTotal: 18070, BalanceSun: 97863827, AllowanceSun: allowance,
			EnergyDelegated: 17270000000, BandwidthStaked: 800000000,
			FetchedAt: day.Add(time.Duration(i) * 5 * time.Minute),
		}
		if err := s.InsertTRXAccount(ctx, a); err != nil {
			t.Fatalf("InsertTRXAccount %d: %v", i, err)
		}
	}

	// 2 条 rental_income 交易（5.32 + 4.37 TRX = 9690000 sun）
	for i, amount := range []int64{5320000, 4370000} {
		if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{
			TxID: "tx_rental_" + string(rune('1'+i)), Ts: day.Add(time.Duration(i) * time.Hour),
			Kind: "rental_income", AmountSun: amount,
		}); err != nil {
			t.Fatalf("UpsertTRXTx %d: %v", i, err)
		}
	}

	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	if err := svc.RollupTrxDailyIncome(ctx, day); err != nil {
		t.Fatalf("RollupTrxDailyIncome: %v", err)
	}

	// 读回 position_daily_income（source='auto'）
	incomes, err := s.ListDailyIncome(ctx, nil, nil, 10, 0)
	if err != nil {
		t.Fatalf("ListDailyIncome: %v", err)
	}
	if len(incomes) != 1 {
		t.Fatalf("got %d income rows, want 1", len(incomes))
	}
	got := incomes[0]
	// F2：voting = Σ正增量 = (150-100) + max(0, 120-150) = 50 + 0 = 50 sun = 0.00005 TRX
	wantVoting := 50.0 / 1e6
	if diff := got.VotingTrx - wantVoting; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("voting_trx = %v, want %v (F2 Σ正增量)", got.VotingTrx, wantVoting)
	}
	// energy = 5.32 + 4.37 = 9.69 TRX（rental_income 之和）
	wantEnergy := 9.69
	if diff := got.EnergyTrx - wantEnergy; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("energy_trx = %v, want %v (rental 之和)", got.EnergyTrx, wantEnergy)
	}
}

// TestRollupTrxDailyIncome_F5PricePnlSnapshot F5 锚：price+pnl 快照复用手工语义。
// 注入 TRXUSDT 报价 + short_hedge 持仓 → rollup 写入 price/pnl。
func TestRollupTrxDailyIncome_F5PricePnlSnapshot(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	// 1 条账户快照
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, AllowanceSun: 100, EnergyDelegated: 17270000000,
		BandwidthStaked: 800000000, FetchedAt: day,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}

	// short_hedge 持仓：entry=0.30 / qty=1000
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO personal_positions (asset, kind, price_symbol, qty, entry_price, leverage, opened_at)
		VALUES ('TRX','short_hedge','TRXUSDT',1000,0.30000,10,'2026-08-01T00:00:00Z')
	`); err != nil {
		t.Fatalf("seed position: %v", err)
	}

	// 注入 TRXUSDT 报价 0.29（新鲜帧）
	qbus := bus.New()
	price := 0.29
	qbus.Publish(bus.Quote{Symbol: "TRXUSDT", Venue: "binance", VenueType: "spot", Bid: price, Ask: price, Time: time.Now()})

	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	if err := svc.RollupTrxDailyIncome(ctx, day); err != nil {
		t.Fatalf("RollupTrxDailyIncome: %v", err)
	}

	incomes, err := s.ListDailyIncome(ctx, nil, nil, 10, 0)
	if err != nil {
		t.Fatalf("ListDailyIncome: %v", err)
	}
	if len(incomes) != 1 {
		t.Fatalf("got %d rows, want 1", len(incomes))
	}
	got := incomes[0]
	// F5：price=0.29（qbus mid）
	if got.Price == nil {
		t.Fatal("price = nil, want 0.29 (F5 qbus 快照)")
	}
	if diff := *got.Price - price; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("price = %v, want %v (F5 qbus 快照)", *got.Price, price)
	}
	// F5：pnl = (entry - price) × qty = (0.30 - 0.29) × 1000 = 10
	wantPnL := (0.30 - 0.29) * 1000
	if got.PnlUSD == nil {
		t.Fatal("pnl_usd = nil, want 10 (F5 空单浮亏)")
	}
	if diff := *got.PnlUSD - wantPnL; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("pnl_usd = %v, want %v (F5 空单浮亏)", *got.PnlUSD, wantPnL)
	}
}

// TestRollupTrxDailyIncome_AutoNotOverwriteManual source='auto' 不覆盖 manual。
// 先写 manual 行 → rollup auto → manual 行保留。
func TestRollupTrxDailyIncome_AutoNotOverwriteManual(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)

	// 先写 manual 行（energy=99.99）
	if _, err := s.AddDailyIncome(ctx, day, 99.99, 0, 0, nil, nil, "manual"); err != nil {
		t.Fatalf("AddDailyIncome manual: %v", err)
	}

	// 1 条账户快照（触发 rollup）
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, AllowanceSun: 100, EnergyDelegated: 17270000000,
		BandwidthStaked: 800000000, FetchedAt: day,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}

	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	if err := svc.RollupTrxDailyIncome(ctx, day); err != nil {
		t.Fatalf("RollupTrxDailyIncome: %v", err)
	}

	incomes, err := s.ListDailyIncome(ctx, nil, nil, 10, 0)
	if err != nil {
		t.Fatalf("ListDailyIncome: %v", err)
	}
	if len(incomes) != 1 {
		t.Fatalf("got %d rows, want 1 (manual 保留)", len(incomes))
	}
	// manual 行保留（energy=99.99，未被 auto 覆盖）
	if incomes[0].EnergyTrx != 99.99 {
		t.Errorf("energy_trx = %v, want 99.99 (manual 保留，auto 不覆盖)", incomes[0].EnergyTrx)
	}
}

// TestD253_RollupInterestFromCMCOvernight D-253 ⑦：rollup auto 行 interest_usd 并入 CMC overnight。
// 有 overnight 记录日 interest_usd= overnight 合计；无记录日照旧 0。
func TestD253_RollupInterestFromCMCOvernight(t *testing.T) {
	s := hedgeTestStore(t) // 含 fin_cmc_history TRUNCATE
	ctx := context.Background()
	day := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)

	// 1 条账户快照（触发 rollup）
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, AllowanceSun: 100, EnergyDelegated: 17270000000,
		BandwidthStaked: 800000000, FetchedAt: day,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}

	// 2 条 CMC overnight 记录（1.65 + 1.50 = 3.15 USD）
	if err := s.UpsertCMCHistory(ctx, 1, []pgstore.CMCHistoryTx{
		{AccountID: 1, TransactionID: "ov1", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 1.65, TransactionTime: day.Add(2 * time.Hour)},
		{AccountID: 1, TransactionID: "ov2", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 1.50, TransactionTime: day.Add(10 * time.Hour)},
	}); err != nil {
		t.Fatalf("UpsertCMCHistory: %v", err)
	}

	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	if err := svc.RollupTrxDailyIncome(ctx, day); err != nil {
		t.Fatalf("RollupTrxDailyIncome: %v", err)
	}

	incomes, err := s.ListDailyIncome(ctx, nil, nil, 10, 0)
	if err != nil {
		t.Fatalf("ListDailyIncome: %v", err)
	}
	if len(incomes) != 1 {
		t.Fatalf("got %d rows, want 1", len(incomes))
	}
	got := incomes[0]
	wantInterest := 1.65 + 1.50 // 3.15
	if diff := got.InterestUSD - wantInterest; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("interest_usd = %v, want %v (CMC overnight 合计 D-253)", got.InterestUSD, wantInterest)
	}
	if !strings.Contains(got.Note, "overnight=") {
		t.Errorf("note should contain overnight=, got %q", got.Note)
	}

	// 无 overnight 记录日照旧 0
	day2 := time.Date(2026, 9, 4, 0, 0, 0, 0, time.UTC)
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		BalanceSun: 97863827, AllowanceSun: 100, EnergyDelegated: 17270000000,
		BandwidthStaked: 800000000, FetchedAt: day2,
	}); err != nil {
		t.Fatalf("InsertTRXAccount day2: %v", err)
	}
	if err := svc.RollupTrxDailyIncome(ctx, day2); err != nil {
		t.Fatalf("RollupTrxDailyIncome day2: %v", err)
	}
	incomes2, err := s.ListDailyIncome(ctx, nil, nil, 10, 0)
	if err != nil {
		t.Fatalf("ListDailyIncome: %v", err)
	}
	// day2 行（最新首行）interest_usd 应为 0（无 overnight 记录）
	if incomes2[0].Day.Format("2006-01-02") != day2.Format("2006-01-02") {
		t.Fatalf("latest row day = %v, want %v", incomes2[0].Day.Format("2006-01-02"), day2.Format("2006-01-02"))
	}
	if incomes2[0].InterestUSD != 0 {
		t.Errorf("day2 interest_usd = %v, want 0 (无 overnight 记录)", incomes2[0].InterestUSD)
	}
}
