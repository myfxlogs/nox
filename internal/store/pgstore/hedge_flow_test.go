// D-212 S1 测试：跨源聚合 store 方法（SumDailyIncomeByRange/SumCMCOvernightByRange/ListHedgeFlowPage）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestSumDailyIncomeByRange_FullSourceAggregate 全源聚合含 manual 纠错行（A4：daily_income 不分 source）。
func TestSumDailyIncomeByRange_FullSourceAggregate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE position_daily_income CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	day1 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	day2 := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	// auto 行 + manual 纠错行（同日覆盖，但 SUM 全量仍计当前值）
	if err := s.AddDailyIncomeAuto(ctx, day1, 5.0, 0.1, 1.5, nil, nil, "auto"); err != nil {
		t.Fatalf("AddDailyIncomeAuto: %v", err)
	}
	if _, err := s.AddDailyIncome(ctx, day2, 10.0, 0.2, 2.5, nil, nil, "manual 纠错"); err != nil {
		t.Fatalf("AddDailyIncome: %v", err)
	}
	// 全量聚合
	energy, voting, interest, err := s.SumDailyIncomeByRange(ctx, nil, nil)
	if err != nil {
		t.Fatalf("SumDailyIncomeByRange: %v", err)
	}
	if energy != 15.0 {
		t.Errorf("energy = %v, want 15.0 (auto 5 + manual 10)", energy)
	}
	if voting != 0.3 {
		t.Errorf("voting = %v, want 0.3", voting)
	}
	if interest != 4.0 {
		t.Errorf("interest = %v, want 4.0", interest)
	}
	// 区间聚合（只含 day1）
	from := day1
	to := day1
	energy, _, _, err = s.SumDailyIncomeByRange(ctx, &from, &to)
	if err != nil {
		t.Fatalf("SumDailyIncomeByRange day1: %v", err)
	}
	if energy != 5.0 {
		t.Errorf("energy [day1] = %v, want 5.0", energy)
	}
}

// TestSumCMCOvernightByRange fin_cmc_history OVERNIGHT_SETTLEMENT 之和。
func TestSumCMCOvernightByRange(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_cmc_history CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	txs := []CMCHistoryTx{
		{AccountID: 1, TransactionID: "h1", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 1.6, TransactionTime: now},
		{AccountID: 1, TransactionID: "h2", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 1.5, TransactionTime: now.Add(time.Hour)},
		{AccountID: 1, TransactionID: "h3", TransactionType: "SELL_TRADE", Amount: 100, TransactionTime: now.Add(2 * time.Hour)}, // 非隔夜息不计
	}
	if err := s.UpsertCMCHistory(ctx, 1, txs); err != nil {
		t.Fatalf("UpsertCMCHistory: %v", err)
	}
	sum, err := s.SumCMCOvernightByRange(ctx, nil, nil)
	if err != nil {
		t.Fatalf("SumCMCOvernightByRange: %v", err)
	}
	if sum != 3.1 {
		t.Errorf("overnight sum = %v, want 3.1 (1.6+1.5, SELL_TRADE 不计)", sum)
	}
}

// TestListHedgeFlowPage_CrossSourceTimeOrder 跨源时间序交错断言（UNION ALL 按 ts DESC）。
func TestListHedgeFlowPage_CrossSourceTimeOrder(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_trx_txs, fin_cmc_history CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	base := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	// TRX 3 条：t1(12:00) t3(12:02) t5(12:04)
	trxTxs := []TRXTx{
		{TxID: "t1", Ts: base, Kind: "rental_income", AmountSun: 5320000},
		{TxID: "t3", Ts: base.Add(2 * time.Minute), Kind: "deposit", AmountSun: 1000000},
		{TxID: "t5", Ts: base.Add(4 * time.Minute), Kind: "vote_claim", AmountSun: 0},
	}
	for _, tx := range trxTxs {
		if err := s.UpsertTRXTx(ctx, tx); err != nil {
			t.Fatalf("UpsertTRXTx %s: %v", tx.TxID, err)
		}
	}
	// CMC 2 条：t2(12:01 overnight) t4(12:03 SELL_TRADE→other)
	cmcTxs := []CMCHistoryTx{
		{AccountID: 1, TransactionID: "c2", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 1.6, TransactionTime: base.Add(time.Minute)},
		{AccountID: 1, TransactionID: "c4", TransactionType: "SELL_TRADE", Amount: 100, TransactionTime: base.Add(3 * time.Minute)},
	}
	if err := s.UpsertCMCHistory(ctx, 1, cmcTxs); err != nil {
		t.Fatalf("UpsertCMCHistory: %v", err)
	}
	// 全部 5 条倒序：t5 c4 t3 c2 t1
	items, err := s.ListHedgeFlowPage(ctx, "", 10, 0)
	if err != nil {
		t.Fatalf("ListHedgeFlowPage: %v", err)
	}
	if len(items) != 5 {
		t.Fatalf("got %d items, want 5", len(items))
	}
	wantOrder := []string{"t5", "c4", "t3", "c2", "t1"}
	for i, w := range wantOrder {
		if items[i].TxID != w {
			t.Errorf("item[%d].txID = %q, want %q (时间序交错)", i, items[i].TxID, w)
		}
	}
	// kind 归一断言
	if items[1].Kind != "other" {
		t.Errorf("c4 kind = %q, want other (SELL_TRADE→other A5)", items[1].Kind)
	}
	if items[3].Kind != "overnight" {
		t.Errorf("c2 kind = %q, want overnight", items[3].Kind)
	}
	// 单位断言（A1：TRX 原值 TRX，CMC USD）
	if items[0].AmountUnit != "TRX" {
		t.Errorf("t5 unit = %q, want TRX", items[0].AmountUnit)
	}
	if items[1].AmountUnit != "USD" {
		t.Errorf("c4 unit = %q, want USD", items[1].AmountUnit)
	}
}

// TestListHedgeFlowPage_SourceFilter 单源筛选。
func TestListHedgeFlowPage_SourceFilter(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_trx_txs, fin_cmc_history CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	if err := s.UpsertTRXTx(ctx, TRXTx{TxID: "tx1", Ts: now, Kind: "rental_income", AmountSun: 1000000}); err != nil {
		t.Fatalf("UpsertTRXTx: %v", err)
	}
	if err := s.UpsertCMCHistory(ctx, 1, []CMCHistoryTx{{AccountID: 1, TransactionID: "c1", TransactionType: "OVERNIGHT_SETTLEMENT", Amount: 1.5, TransactionTime: now}}); err != nil {
		t.Fatalf("UpsertCMCHistory: %v", err)
	}
	// 只 TRX
	trxItems, err := s.ListHedgeFlowPage(ctx, "trx", 10, 0)
	if err != nil {
		t.Fatalf("ListHedgeFlowPage trx: %v", err)
	}
	if len(trxItems) != 1 || trxItems[0].Source != "trx" {
		t.Errorf("trx filter: got %d items, want 1 trx", len(trxItems))
	}
	// 只 CMC
	cmcItems, err := s.ListHedgeFlowPage(ctx, "cmc", 10, 0)
	if err != nil {
		t.Fatalf("ListHedgeFlowPage cmc: %v", err)
	}
	if len(cmcItems) != 1 || cmcItems[0].Source != "cmc" {
		t.Errorf("cmc filter: got %d items, want 1 cmc", len(cmcItems))
	}
}

// TestListHedgeFlowPage_Pagination 分页边界。
func TestListHedgeFlowPage_Pagination(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_trx_txs, fin_cmc_history CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	base := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	for i := 0; i < 15; i++ {
		if err := s.UpsertTRXTx(ctx, TRXTx{
			TxID:      "p" + string(rune('a'+i)),
			Ts:        base.Add(time.Duration(i) * time.Minute),
			Kind:      "rental_income",
			AmountSun: 1000000,
		}); err != nil {
			t.Fatalf("UpsertTRXTx %d: %v", i, err)
		}
	}
	// 第 1 页 10 条（最新 10 条）
	page1, err := s.ListHedgeFlowPage(ctx, "", 10, 0)
	if err != nil {
		t.Fatalf("page1: %v", err)
	}
	if len(page1) != 10 {
		t.Errorf("page1 len = %d, want 10", len(page1))
	}
	// 第 2 页 5 条
	page2, err := s.ListHedgeFlowPage(ctx, "", 10, 10)
	if err != nil {
		t.Fatalf("page2: %v", err)
	}
	if len(page2) != 5 {
		t.Errorf("page2 len = %d, want 5", len(page2))
	}
	// page1[0] 应比 page2[0] 新
	if !page1[0].Ts.After(page2[0].Ts) {
		t.Errorf("page1[0].ts %v should be after page2[0].ts %v", page1[0].Ts, page2[0].Ts)
	}
}
