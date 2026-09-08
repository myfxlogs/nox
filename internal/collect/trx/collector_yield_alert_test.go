// D-258 S2 测试：maybeAlertEnergyRate（能量租金率 floor 告警）。
package trx

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// seedRentalAndAccount 注入 7d rental 数据 + 账户快照。
// rentalPerDaySun=每日 rental sun；energySun=在押 sun。
func seedRentalAndAccount(t *testing.T, st *pgstore.Store, rentalPerDaySun, energySun int64) {
	t.Helper()
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, "DELETE FROM alerts"); err != nil {
		t.Fatalf("cleanup alerts: %v", err)
	}
	now := time.Now().UTC()
	for i := 6; i >= 0; i-- {
		ts := now.Add(-time.Duration(i) * 24 * time.Hour)
		if err := st.UpsertTRXTx(ctx, pgstore.TRXTx{
			TxID: "yield_alert_" + ts.Format("20060102"), Ts: ts,
			Kind: "rental_income", AmountSun: rentalPerDaySun,
		}); err != nil {
			t.Fatalf("UpsertTRXTx %d: %v", i, err)
		}
	}
	if err := st.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", EnergyDelegated: energySun, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
}

// TestMaybeAlertEnergyRate_BelowFloor 低于阈值+样本足 → alert。
// 7d 各 1 TRX（1000000 sun）= 7 TRX 合计；在押 17270 TRX → APR ≈ 2.1% < 8% floor。
func TestMaybeAlertEnergyRate_BelowFloor(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedRentalAndAccount(t, st, 1000000, 17270000000) // 1 TRX/day, 17270 TRX staked
	c := NewTxsCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, "", "", nil, 8.0)
	c.maybeAlertEnergyRate(ctx)
	alerts, err := st.ListAlerts(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) == 0 {
		t.Fatal("expected 1 alert for below-floor rate, got 0")
	}
	if alerts[0].Level != "warn" {
		t.Errorf("alert level = %q, want warn", alerts[0].Level)
	}
}

// TestMaybeAlertEnergyRate_AboveFloor 高于阈值 → 不告。
// 7d 各 5 TRX（5000000 sun）= 35 TRX 合计；在押 17270 TRX → APR ≈ 10.6% > 8% floor。
func TestMaybeAlertEnergyRate_AboveFloor(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, "DELETE FROM alerts"); err != nil {
		t.Fatalf("cleanup alerts: %v", err)
	}
	seedRentalAndAccount(t, st, 5000000, 17270000000) // 5 TRX/day, 17270 TRX staked
	c := NewTxsCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, "", "", nil, 8.0)
	c.maybeAlertEnergyRate(ctx)
	alerts, err := st.ListAlerts(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts for above-floor rate, got %d", len(alerts))
	}
}

// TestMaybeAlertEnergyRate_ColdStart 样本 <7d → 不告。
func TestMaybeAlertEnergyRate_ColdStart(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, "DELETE FROM alerts"); err != nil {
		t.Fatalf("cleanup alerts: %v", err)
	}
	now := time.Now().UTC()
	// 仅 3 天 rental 数据
	for i := 2; i >= 0; i-- {
		ts := now.Add(-time.Duration(i) * 24 * time.Hour)
		if err := st.UpsertTRXTx(ctx, pgstore.TRXTx{
			TxID: "yield_cold_" + ts.Format("20060102"), Ts: ts,
			Kind: "rental_income", AmountSun: 1000000,
		}); err != nil {
			t.Fatalf("UpsertTRXTx %d: %v", i, err)
		}
	}
	if err := st.InsertTRXAccount(ctx, pgstore.TRXAccount{
		Address: "TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", EnergyDelegated: 17270000000, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertTRXAccount: %v", err)
	}
	c := NewTxsCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, "", "", nil, 8.0)
	c.maybeAlertEnergyRate(ctx)
	alerts, err := st.ListAlerts(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts for cold start (<7d), got %d", len(alerts))
	}
}

// TestMaybeAlertEnergyRate_DailyThrottle 当日节流：第二次调不重复告警。
func TestMaybeAlertEnergyRate_DailyThrottle(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	seedRentalAndAccount(t, st, 1000000, 17270000000) // below floor
	c := NewTxsCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, "", "", nil, 8.0)
	c.maybeAlertEnergyRate(ctx)
	c.maybeAlertEnergyRate(ctx) // 第二次，应节流
	alerts, err := st.ListAlerts(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 1 {
		t.Errorf("expected 1 alert (daily throttle), got %d", len(alerts))
	}
}

// TestMaybeAlertEnergyRate_FloorDisabled floor=0 → 不评估不告。
func TestMaybeAlertEnergyRate_FloorDisabled(t *testing.T) {
	st := newTestStore(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, "DELETE FROM alerts"); err != nil {
		t.Fatalf("cleanup alerts: %v", err)
	}
	seedRentalAndAccount(t, st, 1000000, 17270000000) // below floor but floor=0
	c := NewTxsCollector("TWsbXR6SPvvXPxUtPMQvEj7AMH55555555", st, "", "", nil, 0)
	c.maybeAlertEnergyRate(ctx)
	alerts, err := st.ListAlerts(ctx, 10, 0)
	if err != nil {
		t.Fatalf("ListAlerts: %v", err)
	}
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts when floor=0 (disabled), got %d", len(alerts))
	}
}
