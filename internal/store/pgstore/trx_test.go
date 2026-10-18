// 测试：migration 0068 + store 方法 + F1 锚 + P2-2 vote_claim amount=0。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// TestTRXAccountInsertAndLast 验证 fin_trx_account 写入+查询最新。
func TestTRXAccountInsertAndLast(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_trx_account CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now().UTC()
	sr := "TNeEwWHXLLUgEtfzTnYN8wtVenGxuMzZCE"
	a := TRXAccount{
		Address:         "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555",
		VotesTotal:      18070,
		TronPower:       107332000000, // sun（= 107332 TRX）
		SRAddress:       &sr,
		BalanceSun:      97863827,
		AllowanceSun:    7717994,
		EnergyDelegated: 17270000000,
		BandwidthStaked: 800000000,
		NetLimit:        1286,
		FetchedAt:       now,
	}
	if err := s.InsertTRXAccount(ctx, a); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	// 写第二条更早的，验证 Last 取最新
	a2 := a
	a2.FetchedAt = now.Add(-10 * time.Minute)
	a2.BalanceSun = 90000000
	if err := s.InsertTRXAccount(ctx, a2); err != nil {
		t.Fatalf("InsertTRXAccount 2: %v", err)
	}
	got, err := s.LastTRXAccount(ctx)
	if err != nil {
		t.Fatalf("LastTRXAccount: %v", err)
	}
	if got.BalanceSun != 97863827 {
		t.Errorf("BalanceSun = %d, want 97863827 (latest)", got.BalanceSun)
	}
	if got.VotesTotal != 18070 {
		t.Errorf("VotesTotal = %d, want 18070", got.VotesTotal)
	}
	if got.TronPower != 107332000000 {
		t.Errorf("TronPower = %d, want 107332000000", got.TronPower)
	}
	if got.SRAddress == nil || *got.SRAddress != sr {
		t.Errorf("SRAddress = %v, want %s", got.SRAddress, sr)
	}
}

// TestTRXTxUpsertIdempotent 验证 tx_id UNIQUE 幂等去重。
func TestTRXTxUpsertIdempotent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_trx_txs CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	cp := "TBtvnRBgrVE9zZWkWfPvDSiuo65es55555"
	tx := TRXTx{
		TxID:         "abc123",
		Ts:           time.Now().UTC(),
		Kind:         "rental_income",
		AmountSun:    5320000,
		Counterparty: &cp,
	}
	if err := s.UpsertTRXTx(ctx, tx); err != nil {
		t.Fatalf("UpsertTRXTx 1: %v", err)
	}
	// 重复写同一 tx_id → no-op（ON CONFLICT DO NOTHING）
	tx.AmountSun = 9999999 // 改值验证不覆盖
	if err := s.UpsertTRXTx(ctx, tx); err != nil {
		t.Fatalf("UpsertTRXTx 2: %v", err)
	}
	txs, err := s.ListTRXTxs(ctx, 10, "")
	if err != nil {
		t.Fatalf("ListTRXTxs: %v", err)
	}
	if len(txs) != 1 {
		t.Fatalf("got %d txs, want 1 (idempotent)", len(txs))
	}
	if txs[0].AmountSun != 5320000 {
		t.Errorf("AmountSun = %d, want 5320000 (first write preserved)", txs[0].AmountSun)
	}
}

// TestTRXTxVoteClaimAmountZero P2-2：vote_claim 行 amount_sun=0（WithdrawBalanceContract 无金额参数）。
func TestTRXTxVoteClaimAmountZero(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_trx_txs CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	tx := TRXTx{
		TxID:      "vote_claim_001",
		Ts:        time.Now().UTC(),
		Kind:      "vote_claim",
		AmountSun: 0, // P2-2：vote_claim amount=0
	}
	if err := s.UpsertTRXTx(ctx, tx); err != nil {
		t.Fatalf("UpsertTRXTx vote_claim: %v", err)
	}
	txs, err := s.ListTRXTxs(ctx, 10, "")
	if err != nil {
		t.Fatalf("ListTRXTxs: %v", err)
	}
	if len(txs) != 1 || txs[0].Kind != "vote_claim" {
		t.Fatalf("got %d txs, want 1 vote_claim", len(txs))
	}
	if txs[0].AmountSun != 0 {
		t.Errorf("vote_claim AmountSun = %d, want 0 (P2-2)", txs[0].AmountSun)
	}
}

// TestSumTRXTxRentalByDay 验证按日汇总 rental_income。
func TestSumTRXTxRentalByDay(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_trx_txs CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	cp := "TBtvnRBgrVE9zZWkWfPvDSiuo65es55555"
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	// 写 3 笔 rental（2 笔在 D 日，1 笔在 D+1 日）
	for _, ts := range []time.Time{
		day.Add(16 * time.Hour), // D 日 16:00
		day.Add(20 * time.Hour), // D 日 20:00
		day.Add(36 * time.Hour), // D+1 日 12:00
	} {
		if err := s.UpsertTRXTx(ctx, TRXTx{
			TxID: "r" + ts.Format("150405"), Ts: ts, Kind: "rental_income",
			AmountSun: 5000000, Counterparty: &cp,
		}); err != nil {
			t.Fatalf("UpsertTRXTx: %v", err)
		}
	}
	// D 日 rental 之和 = 2 笔 × 5000000 = 10000000
	got, err := s.SumTRXTxRentalByDay(ctx, day)
	if err != nil {
		t.Fatalf("SumTRXTxRentalByDay: %v", err)
	}
	if got != 10000000 {
		t.Errorf("D day rental sum = %d, want 10000000", got)
	}
}

// TestSumTRXTxRentalByRange D-258 S1：区间汇总 rental_income。
func TestSumTRXTxRentalByRange(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE fin_trx_txs CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	cp := "TBtvnRBgrVE9zZWkWfPvDSiuo65es55555"
	day := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	// 写 4 笔 rental：D 日 2 笔，D+1 日 1 笔，D+2 日 1 笔
	for _, ts := range []time.Time{
		day.Add(2 * time.Hour),  // D 日 02:00
		day.Add(10 * time.Hour), // D 日 10:00
		day.Add(30 * time.Hour), // D+1 日 06:00
		day.Add(54 * time.Hour), // D+2 日 06:00
	} {
		if err := s.UpsertTRXTx(ctx, TRXTx{
			TxID: "rr" + ts.Format("150405"), Ts: ts, Kind: "rental_income",
			AmountSun: 3000000, Counterparty: &cp,
		}); err != nil {
			t.Fatalf("UpsertTRXTx: %v", err)
		}
	}
	// [D, D+2) = D+D+1 = 3 笔 × 3000000 = 9000000
	got, err := s.SumTRXTxRentalByRange(ctx, day, day.Add(48*time.Hour))
	if err != nil {
		t.Fatalf("SumTRXTxRentalByRange: %v", err)
	}
	if got != 9000000 {
		t.Errorf("range [D,D+2) = %d, want 9000000", got)
	}
	// from >= to → 0
	got, _ = s.SumTRXTxRentalByRange(ctx, day.Add(48*time.Hour), day)
	if got != 0 {
		t.Errorf("from>=to should return 0, got %d", got)
	}
}

// TestAddDailyIncomeAutoF1Anchor F1 锚：auto 行→手工纠错→再 rollup 不回退（数值保持手工值）。
func TestAddDailyIncomeAutoF1Anchor(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE position_daily_income CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	day := time.Date(2026, 9, 2, 0, 0, 0, 0, time.UTC)
	price := 0.15
	pnl := -1.2

	// 1. auto rollup 写入
	if err := s.AddDailyIncomeAuto(ctx, day, 5.32, 0.5, 0, &price, &pnl, "auto: energy=1笔"); err != nil {
		t.Fatalf("AddDailyIncomeAuto: %v", err)
	}
	rows, _ := s.ListDailyIncome(ctx, nil, nil, 10, 0)
	if len(rows) != 1 || rows[0].Source != "auto" || rows[0].EnergyTrx != 5.32 {
		t.Fatalf("after auto: got %+v, want source=auto energy=5.32", rows)
	}

	// 2. 业主手工纠错（AddDailyIncome stamp source='manual'）
	if _, err := s.AddDailyIncome(ctx, day, 6.0, 0.5, 0, &price, &pnl, "manual correction"); err != nil {
		t.Fatalf("AddDailyIncome manual: %v", err)
	}
	rows, _ = s.ListDailyIncome(ctx, nil, nil, 10, 0)
	if len(rows) != 1 || rows[0].Source != "manual" || rows[0].EnergyTrx != 6.0 {
		t.Fatalf("after manual: got %+v, want source=manual energy=6.0", rows)
	}

	// 3. 再 rollup（auto 路径条件更新 WHERE source='auto'）→ 不触碰 manual 行
	if err := s.AddDailyIncomeAuto(ctx, day, 5.32, 0.5, 0, &price, &pnl, "auto: re-rollup"); err != nil {
		t.Fatalf("AddDailyIncomeAuto re-rollup: %v", err)
	}
	rows, _ = s.ListDailyIncome(ctx, nil, nil, 10, 0)
	if len(rows) != 1 || rows[0].Source != "manual" || rows[0].EnergyTrx != 6.0 {
		t.Fatalf("after re-rollup: got %+v, want source=manual energy=6.0 (纠错不回退)", rows)
	}
	if rows[0].Note != "manual correction" {
		t.Errorf("Note = %q, want 'manual correction' (auto 不覆盖 manual note)", rows[0].Note)
	}
}

// TestAddDailyIncomeSourceStamp 验证手工路径双向盖章 source='manual'。
func TestAddDailyIncomeSourceStamp(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, "TRUNCATE position_daily_income CASCADE"); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	day := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	// 首次手工写入
	id1, err := s.AddDailyIncome(ctx, day, 5.0, 0.3, 0, nil, nil, "first")
	if err != nil {
		t.Fatalf("AddDailyIncome 1: %v", err)
	}
	// 再次手工写入（覆盖）→ source 仍 'manual'
	id2, err := s.AddDailyIncome(ctx, day, 6.0, 0.4, 0, nil, nil, "second")
	if err != nil {
		t.Fatalf("AddDailyIncome 2: %v", err)
	}
	if id1 != id2 {
		t.Errorf("id changed: %d → %d (upsert should keep id)", id1, id2)
	}
	rows, _ := s.ListDailyIncome(ctx, nil, nil, 10, 0)
	if len(rows) != 1 || rows[0].Source != "manual" {
		t.Errorf("source = %q, want 'manual' (双向盖章)", rows[0].Source)
	}
	if rows[0].EnergyTrx != 6.0 {
		t.Errorf("EnergyTrx = %f, want 6.0 (覆盖)", rows[0].EnergyTrx)
	}
}
