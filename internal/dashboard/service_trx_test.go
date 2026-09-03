// D-211 S3 测试：RollupTrxDailyIncome（F2 Σ正增量/F5 price+pnl 快照）+ ListTRXAccount RPC。
package dashboard

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5"
)

// trxTestStore 创建测试 store（ARB_TEST_PG_SERVICE_DSN，TRUNCATE 全库）。
func trxTestStore(t *testing.T) *pgstore.Store {
	t.Helper()
	dsn := os.Getenv("ARB_TEST_PG_SERVICE_DSN")
	if dsn == "" {
		t.Skip("ARB_TEST_PG_SERVICE_DSN 未设置，跳过需真库的测试")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	if !strings.Contains(cfg.Database, "test") {
		t.Fatalf("DSN 指向非测试库 %q：测试会 TRUNCATE 该库，拒绝执行", cfg.Database)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	s, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// TRUNCATE 相关表
	for _, tbl := range []string{"fin_trx_account", "fin_trx_txs", "fin_trx_witness", "position_daily_income", "personal_positions"} {
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
			Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555",
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

// TestListTRXAccount_RPCEmpty 验证空表容忍（summary=nil，txs 空数组）。
func TestListTRXAccount_RPCEmpty(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}

	resp, err := svc.ListTRXAccount(ctx, connect.NewRequest(&v1.ListTRXAccountRequest{}))
	if err != nil {
		t.Fatalf("ListTRXAccount: %v", err)
	}
	if resp.Msg.Summary != nil {
		t.Errorf("summary = %v, want nil (空表)", resp.Msg.Summary)
	}
	if len(resp.Msg.Txs) != 0 {
		t.Errorf("txs len = %d, want 0 (空表)", len(resp.Msg.Txs))
	}
}

// TestListTRXAccount_RPCWithData 验证有数据时 summary+txs 正确返回。
func TestListTRXAccount_RPCWithData(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()

	// 1 条账户快照
	srAddr := "TNeEwWHXLLUgEtfzTnYN8wtVenGxuMzZCE"
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		SRAddress: &srAddr, BalanceSun: 97863827, AllowanceSun: 7717994,
		EnergyDelegated: 17270000000, BandwidthStaked: 800000000,
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	// 2 条交易
	cp := "TBtvnRBgrVE9zZWkWfPvDSiuo65es55555"
	if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{
		TxID: "tx1", Ts: time.Now(), Kind: "rental_income", AmountSun: 5320000, Counterparty: &cp,
	}); err != nil {
		t.Fatalf("UpsertTRXTx 1: %v", err)
	}
	if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{
		TxID: "tx2", Ts: time.Now().Add(-time.Hour), Kind: "vote_claim", AmountSun: 0,
	}); err != nil {
		t.Fatalf("UpsertTRXTx 2: %v", err)
	}

	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	resp, err := svc.ListTRXAccount(ctx, connect.NewRequest(&v1.ListTRXAccountRequest{}))
	if err != nil {
		t.Fatalf("ListTRXAccount: %v", err)
	}
	if resp.Msg.Summary == nil {
		t.Fatal("summary = nil, want non-nil")
	}
	// balance = 97863827 sun = 97.863827 TRX
	wantBalance := 97.863827
	if diff := resp.Msg.Summary.BalanceTrx - wantBalance; diff > 1e-6 || diff < -1e-6 {
		t.Errorf("balance_trx = %v, want %v", resp.Msg.Summary.BalanceTrx, wantBalance)
	}
	if resp.Msg.Summary.VotesTotal != 18070 {
		t.Errorf("votes_total = %d, want 18070", resp.Msg.Summary.VotesTotal)
	}
	if resp.Msg.Summary.SrAddress != srAddr {
		t.Errorf("sr_address = %q, want %q", resp.Msg.Summary.SrAddress, srAddr)
	}
	if len(resp.Msg.Txs) != 2 {
		t.Fatalf("txs len = %d, want 2", len(resp.Msg.Txs))
	}
	// 第一条（最新）= rental_income
	if resp.Msg.Txs[0].Kind != "rental_income" {
		t.Errorf("txs[0] kind = %q, want rental_income", resp.Msg.Txs[0].Kind)
	}
	if resp.Msg.Txs[0].AmountTrx != 5.32 {
		t.Errorf("txs[0] amount = %v, want 5.32", resp.Msg.Txs[0].AmountTrx)
	}
}
