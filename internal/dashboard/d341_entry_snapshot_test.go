package dashboard

// D-341 T4：开仓学习样本锚——EnsureEntryGateSnapshot 落 positions 两列（IS NULL 幂等+缺行不写）。
import (
	"context"
	"testing"
)

func TestD341_EntryGateSnapshot(t *testing.T) {
	s := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, fin_xasset_monitor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	pos := pgstorePositionHelper()
	posID, err := s.CreatePosition(ctx, &pos)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// monitor 行（noise=9/floor=7）
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_xasset_monitor (pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, harvest_floor_bps)
		VALUES ('XAUUSDT-XAU', 4355, 4356, 4354, 4355, 12, 5, 3, 0.001, 21.9, 1.0, 24.9, 9, 7)`); err != nil {
		t.Fatalf("seed monitor: %v", err)
	}
	if err := s.EnsureEntryGateSnapshot(ctx, posID, "XAUUSDT-XAU", "binance:perp", "3001157:mt5", entryGateSnapshotMaxAge); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	var noise, floor *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT entry_noise_bps, entry_harvest_floor_bps FROM positions WHERE id=$1`, posID).Scan(&noise, &floor); err != nil {
		t.Fatalf("query: %v", err)
	}
	if noise == nil || *noise != 9 || floor == nil || *floor != 7 {
		t.Errorf("T4 学习锚 want noise=9/floor=7, got %v/%v", noise, floor)
	}
	// IS NULL 幂等：改 monitor 行后重调不覆写
	if _, err := s.Pool().Exec(ctx, `UPDATE fin_xasset_monitor SET noise_bps=99`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureEntryGateSnapshot(ctx, posID, "XAUUSDT-XAU", "binance:perp", "3001157:mt5", entryGateSnapshotMaxAge); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT entry_noise_bps FROM positions WHERE id=$1`, posID).Scan(&noise); err != nil {
		t.Fatal(err)
	}
	if noise == nil || *noise != 9 {
		t.Errorf("幂等：首因为准 want 9, got %v", noise)
	}
	// 缺行（无 monitor 行的新仓）→ 不写（NULL 诚实缺样本）
	pos2v := pgstorePositionHelper()
	pos2, err := s.CreatePosition(ctx, &pos2v)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_xasset_monitor`); err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureEntryGateSnapshot(ctx, pos2, "XAUUSDT-XAU", "binance:perp", "3001157:mt5", entryGateSnapshotMaxAge); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT entry_noise_bps FROM positions WHERE id=$1`, pos2).Scan(&noise); err != nil {
		t.Fatal(err)
	}
	if noise != nil {
		t.Errorf("缺行不写 want NULL, got %v", *noise)
	}
}
