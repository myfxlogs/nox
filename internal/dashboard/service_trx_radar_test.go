// D-258 S1 测试：TrxEnergyRentalAPR（能量租金年化收益率）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// TestTrxEnergyRentalAPR_7d 7d trailing 年化精确断言。
// 7 天 rental 各 5 TRX（5000000 sun）= 35 TRX 合计；在押 17270 TRX（17270000000 sun）。
// APR = (35/7×365)/17270×100 ≈ 10.565%。
func TestTrxEnergyRentalAPR_7d(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// 7 天 rental_income（每天 1 笔，各 5 TRX = 5000000 sun）
	for i := 6; i >= 0; i-- {
		ts := now.Add(-time.Duration(i) * 24 * time.Hour)
		if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{
			TxID: "rental_7d_" + ts.Format("20060102"), Ts: ts,
			Kind: "rental_income", AmountSun: 5000000,
		}); err != nil {
			t.Fatalf("UpsertTRXTx %d: %v", i, err)
		}
	}
	// 账户快照：在押 17270 TRX
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", VotesTotal: 18070,
		EnergyDelegated: 17270000000, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	apr, sampleDays, err := svc.TrxEnergyRentalAPR(ctx, 7)
	if err != nil {
		t.Fatalf("TrxEnergyRentalAPR: %v", err)
	}
	if sampleDays < 7 {
		t.Fatalf("sampleDays = %d, want >= 7", sampleDays)
	}
	// APR = (35/7×365)/17270×100 = 10.5659...
	wantApr := (35.0 / 7.0 * 365) / 17270 * 100
	if diff := apr - wantApr; diff > 0.1 || diff < -0.1 {
		t.Errorf("apr = %.4f, want ~%.4f", apr, wantApr)
	}
}

// TestTrxEnergyRentalAPR_ColdStart 样本不足 → sampleDays < N，aprPct=0 不猜。
func TestTrxEnergyRentalAPR_ColdStart(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// 仅 3 天 rental 数据
	for i := 2; i >= 0; i-- {
		ts := now.Add(-time.Duration(i) * 24 * time.Hour)
		if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{
			TxID: "rental_cold_" + ts.Format("20060102"), Ts: ts,
			Kind: "rental_income", AmountSun: 5000000,
		}); err != nil {
			t.Fatalf("UpsertTRXTx %d: %v", i, err)
		}
	}
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", EnergyDelegated: 17270000000, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	apr, sampleDays, err := svc.TrxEnergyRentalAPR(ctx, 7)
	if err != nil {
		t.Fatalf("TrxEnergyRentalAPR: %v", err)
	}
	if sampleDays >= 7 {
		t.Errorf("sampleDays = %d, want < 7 (cold start)", sampleDays)
	}
	if apr != 0 {
		t.Errorf("apr = %v, want 0 (cold start, 不猜)", apr)
	}
}

// TestTrxEnergyRentalAPR_Empty 空表 → sampleDays=0，aprPct=0。
func TestTrxEnergyRentalAPR_Empty(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	apr, sampleDays, err := svc.TrxEnergyRentalAPR(ctx, 7)
	if err != nil {
		t.Fatalf("TrxEnergyRentalAPR: %v", err)
	}
	if sampleDays != 0 {
		t.Errorf("sampleDays = %d, want 0 (empty)", sampleDays)
	}
	if apr != 0 {
		t.Errorf("apr = %v, want 0 (empty)", apr)
	}
}

// TestTrxEnergyRentalAPR_ZeroDenominator 在押=0 → aprPct=0，sampleDays 正常。
func TestTrxEnergyRentalAPR_ZeroDenominator(t *testing.T) {
	s := trxTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()
	for i := 6; i >= 0; i-- {
		ts := now.Add(-time.Duration(i) * 24 * time.Hour)
		if err := s.UpsertTRXTx(ctx, pgstore.TRXTx{
			TxID: "rental_zero_" + ts.Format("20060102"), Ts: ts,
			Kind: "rental_income", AmountSun: 5000000,
		}); err != nil {
			t.Fatalf("UpsertTRXTx %d: %v", i, err)
		}
	}
	// 在押 = 0
	if err := s.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", EnergyDelegated: 0, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	qbus := bus.New()
	svc := &Service{store: s, qbus: qbus, freshTTL: time.Minute, liqMMR: 0.005}
	apr, sampleDays, err := svc.TrxEnergyRentalAPR(ctx, 7)
	if err != nil {
		t.Fatalf("TrxEnergyRentalAPR: %v", err)
	}
	if sampleDays < 7 {
		t.Errorf("sampleDays = %d, want >= 7", sampleDays)
	}
	if apr != 0 {
		t.Errorf("apr = %v, want 0 (zero denominator)", apr)
	}
}
