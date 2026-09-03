// D-208 v2 测试：CMC 视图函数 + 监视器路由 + collector 增强。
// DSN-gated（testutil.TestDSN + Migrate）——非 SKIP 才算 PASS。
package position

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

// fakeCMCSource 内存实现 CMCSource 接口（测试注入，无需真库）。
type fakeCMCSource struct {
	mid       float64
	fetchedAt time.Time
	midErr    error
	closeout  float64
	distPct   float64
	closeErr  error
	pnl       float64
	pnlErr    error
	pos       pgstore.CMCPosition
	posErr    error
}

func (f *fakeCMCSource) LastCMCMid(string) (float64, time.Time, error) {
	return f.mid, f.fetchedAt, f.midErr
}
func (f *fakeCMCSource) LastCMCCloseout() (float64, float64, error) {
	return f.closeout, f.distPct, f.closeErr
}
func (f *fakeCMCSource) LastCMCUnrealised(string) (float64, error) {
	return f.pnl, f.pnlErr
}
func (f *fakeCMCSource) LastCMCPosition(string) (pgstore.CMCPosition, error) {
	return f.pos, f.posErr
}

// === 1. TestComputeCMCPositionPrice_Live（真库 DSN-gated） ===

func TestComputeCMCPositionPrice_Live(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	// 清空 cmc 表
	for _, tbl := range []string{"fin_cmc_prices", "fin_cmc_account_summary", "fin_cmc_positions"} {
		if _, err := st.Pool().Exec(ctx, "DELETE FROM "+tbl); err != nil {
			t.Fatalf("cleanup %s: %v", tbl, err)
		}
	}
	now := time.Now().UTC().Truncate(time.Second)

	// 插 fin_cmc_prices（mid 0.3240，fetched_at=now）
	if err := st.InsertCMCPrice(ctx, pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", MidPrice: 0.3240, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertCMCPrice: %v", err)
	}
	// 插 fin_cmc_account_summary（closeout 0.4226, distance 30.57 百分数）
	if err := st.InsertCMCAccountSummary(ctx, pgstore.CMCAccountSummary{
		AccountID: 1, CloseoutPrice: 0.4226, DistanceToCloseoutPct: 30.57, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertCMCAccountSummary: %v", err)
	}
	// 插 fin_cmc_positions（unrealised 117.83, entry 0.3240, qty 5000, margin_pct 0.01）
	if err := st.InsertCMCPositions(ctx, 1, []pgstore.CMCPosition{{
		InstrumentID: "X-AFQUL", Direction: "SELL", Quantity: 5000, EntryPrice: 0.3240,
		UnrealisedPnL: 117.83, MarginPercentage: 0.01, FetchedAt: now,
	}}); err != nil {
		t.Fatalf("InsertCMCPositions: %v", err)
	}

	// 构造 personal_positions 行（cmc_instrument_id='X-AFQUL', warn_high=0.38, crit_high=0.40）
	warnHigh, critHigh := 0.38, 0.40
	p := &pgstore.PersonalPosition{
		ID: 1, Asset: "TRX", Kind: "short_hedge", PriceSymbol: "TRXUSDT",
		WarnHigh: &warnHigh, CritHigh: &critHigh, Status: "OPEN",
		CMCInstrumentID: strPtr("X-AFQUL"),
	}

	// 用 CMCSourceAdapter 适配真库
	src := &pgstore.CMCSourceAdapter{Store: st, Ctx: ctx}
	v := ComputeCMCPositionPrice(p, src, 10*time.Second, now)

	// 断言
	if v.Price != 0.3240 {
		t.Errorf("Price: got %v, want 0.3240", v.Price)
	}
	if v.LiqPrice != 0.4226 {
		t.Errorf("LiqPrice: got %v, want 0.4226", v.LiqPrice)
	}
	if v.PnlUsd != 117.83 {
		t.Errorf("PnlUsd: got %v, want 117.83", v.PnlUsd)
	}
	// DistToLiqPct：CMC distance=30.57 百分数 → 换算小数 0.3057
	// 自算口径：(0.4226-0.3240)/0.3240 = 0.3043（同量级交叉断言）
	if v.DistToLiqPct <= 0.30 || v.DistToLiqPct >= 0.31 {
		t.Errorf("DistToLiqPct: got %v, want ~0.3057 (30.57%%/100)", v.DistToLiqPct)
	}
	// Level：price 0.3240 < warn 0.38 → ok
	if v.Level != "ok" {
		t.Errorf("Level: got %q, want ok", v.Level)
	}
}

// === 2. TestComputeCMCPositionPrice_StaleEmpty ===

func TestComputeCMCPositionPrice_StaleEmpty(t *testing.T) {
	now := time.Now()
	staleTime := now.Add(-20 * time.Second) // 超 10s TTL
	src := &fakeCMCSource{
		mid:       0.3240,
		fetchedAt: staleTime,
	}
	p := &pgstore.PersonalPosition{
		ID: 1, CMCInstrumentID: strPtr("X-AFQUL"), Status: "OPEN",
	}
	v := ComputeCMCPositionPrice(p, src, 10*time.Second, now)
	if v.Price != 0 {
		t.Errorf("Stale Price: got %v, want 0 (honest empty)", v.Price)
	}
	if v.Level != "" {
		t.Errorf("Stale Level: got %q, want empty", v.Level)
	}
}

// === 3. TestPersonalMonitor_CMCRouting ===

func TestPersonalMonitor_CMCRouting(t *testing.T) {
	// 构造 cmc 行 + 非 cmc 行混合，cmcSource 注入 → 各走各路径
	// 此测试用 fakeCMCSource，无需真库（路由逻辑不依赖 DB）
	src := &fakeCMCSource{
		mid:       0.3240,
		fetchedAt: time.Now(),
		closeout:  0.4226,
		distPct:   30.57,
		pnl:       117.83,
	}
	warnHigh, critHigh := 0.38, 0.40
	cmcPos := &pgstore.PersonalPosition{
		ID: 1, Asset: "TRX", Kind: "short_hedge", PriceSymbol: "TRXUSDT",
		WarnHigh: &warnHigh, CritHigh: &critHigh, Status: "OPEN",
		CMCInstrumentID: strPtr("X-AFQUL"),
	}
	nonCMCPos := &pgstore.PersonalPosition{
		ID: 2, Asset: "TRX", Kind: "staking", PriceSymbol: "TRXUSDT",
		Status: "OPEN", // 无 CMCInstrumentID → 走老路径
	}

	// CMC 行 → ComputeCMCPositionPrice
	vCMC := ComputeCMCPositionPrice(cmcPos, src, 10*time.Second, time.Now())
	if vCMC.Price != 0.3240 {
		t.Errorf("CMC row Price: got %v, want 0.3240", vCMC.Price)
	}
	if vCMC.LiqPrice != 0.4226 {
		t.Errorf("CMC row LiqPrice: got %v, want 0.4226", vCMC.LiqPrice)
	}

	// 非 CMC 行 → ComputeCMCPositionPrice 返回空视图（调用方应走老路径）
	vNon := ComputeCMCPositionPrice(nonCMCPos, src, 10*time.Second, time.Now())
	if vNon.Price != 0 {
		t.Errorf("Non-CMC row Price: got %v, want 0 (should use old path)", vNon.Price)
	}
}

// === 4. TestComputeCMCPositionPrice_NilSource ===

func TestComputeCMCPositionPrice_NilSource(t *testing.T) {
	p := &pgstore.PersonalPosition{
		ID: 1, CMCInstrumentID: strPtr("X-AFQUL"), Status: "OPEN",
	}
	// cmcSource=nil + cmc 行 → 空视图不 panic
	v := ComputeCMCPositionPrice(p, nil, 10*time.Second, time.Now())
	if v.Price != 0 {
		t.Errorf("NilSource Price: got %v, want 0", v.Price)
	}
	if v.Level != "" {
		t.Errorf("NilSource Level: got %q, want empty", v.Level)
	}
}

// === v2 验收锚增补：CopyFrom 持久化锚（回读断言 opened_at/order_id 已落库） ===

func TestInsertCMCPositions_CopyFrom_OpenedAtOrderID(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, "DELETE FROM fin_cmc_positions"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	openedAt := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
	if err := st.InsertCMCPositions(ctx, 1, []pgstore.CMCPosition{{
		InstrumentID: "X-AFQUL", Direction: "SELL", Quantity: 5000, EntryPrice: 0.3240,
		UnrealisedPnL: 117.83, MarginPercentage: 0.01, OpenedAt: &openedAt, OrderID: "ORD-123",
		FetchedAt: time.Now(),
	}}); err != nil {
		t.Fatalf("InsertCMCPositions: %v", err)
	}
	// 回读断言 opened_at/order_id 已落库（防列清单漏更的静默丢弃）
	pos, err := st.LastCMCPositionByInstrument(ctx, "X-AFQUL")
	if err != nil {
		t.Fatalf("LastCMCPositionByInstrument: %v", err)
	}
	if pos.OrderID != "ORD-123" {
		t.Errorf("OrderID: got %q, want ORD-123", pos.OrderID)
	}
	if pos.OpenedAt == nil {
		t.Fatal("OpenedAt: got nil, want non-nil")
	}
	if !pos.OpenedAt.Equal(openedAt) {
		t.Errorf("OpenedAt: got %v, want %v", *pos.OpenedAt, openedAt)
	}
}

// === v2 验收锚增补：HistoryCollector 全类型入库（SELL_TRADE/PAYMENTS_IN 不再被丢） ===

func TestHistoryCollector_AllTypes(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_TEST_PG_DSN")
	ctx := context.Background()
	st, err := pgstore.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := st.Migrate(ctx); err != nil {
		t.Fatalf("Migrate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, "DELETE FROM fin_cmc_history"); err != nil {
		t.Fatalf("cleanup: %v", err)
	}
	// 插入混合类型：OVERNIGHT_SETTLEMENT + SELL_TRADE + PAYMENTS_IN
	txs := []pgstore.CMCHistoryTx{
		{AccountID: 1, TransactionID: "TX1", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 0.5, Balance: 100, Currency: "USD", TransactionTime: time.Now()},
		{AccountID: 1, TransactionID: "TX2", TransactionType: "SELL_TRADE", Amount: -500, Balance: 2000, Currency: "USD", TransactionTime: time.Now()},
		{AccountID: 1, TransactionID: "TX3", TransactionType: "PAYMENTS_IN", Amount: 1000, Balance: 3000, Currency: "USD", TransactionTime: time.Now()},
	}
	if err := st.UpsertCMCHistory(ctx, 1, txs); err != nil {
		t.Fatalf("UpsertCMCHistory: %v", err)
	}
	// 回读全部 3 条
	history, err := st.ListCMCHistory(ctx, 1, 50)
	if err != nil {
		t.Fatalf("ListCMCHistory: %v", err)
	}
	if len(history) != 3 {
		t.Errorf("history count: got %d, want 3 (all types should be persisted)", len(history))
	}
	// 确认 SELL_TRADE 和 PAYMENTS_IN 都在
	types := map[string]bool{}
	for _, h := range history {
		types[h.TransactionType] = true
	}
	for _, want := range []string{"OVERNIGHT_SETTLEMENT", "SELL_TRADE", "PAYMENTS_IN"} {
		if !types[want] {
			t.Errorf("missing type %q in history", want)
		}
	}
}

func strPtr(s string) *string { return &s }
