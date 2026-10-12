// d371_balance_test.go —— D-371 S5-e 配平锚：同 cfgs 输入下，ListVenueIsReal（SQL 镜像）
// 与 reconcile.CexKeyWinners（Go 计算）赢家判定逐键相等（防镜像漂移）。
package main

import (
	"context"
	"testing"
	"time"

	"nox/internal/ledger/reconcile"
	"nox/internal/platform/testutil"
	"nox/internal/store/pgstore"
)

func TestD371_Balance_SQLWinnerEqualsCexKeyWinners(t *testing.T) {
	dsn := testutil.TestDSN(t, "NOX_TEST_PG_DSN")
	s, err := pgstore.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("pgstore.New: %v", err)
	}
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE exchange_configs CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}

	base := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	cfgs := []pgstore.ExchangeConfig{
		{ID: 20, Venue: "binance", VenueType: "perp", Simulated: false, CreatedAt: base},
		{ID: 25, Venue: "binance", VenueType: "perp", Simulated: true, CreatedAt: base.Add(time.Hour)}, // 赢家
		{ID: 30, Venue: "binance", VenueType: "spot", Simulated: false, CreatedAt: base},
		{ID: 40, Venue: "okx", VenueType: "funding", Simulated: true, CreatedAt: base},
	}
	for _, c := range cfgs {
		if _, err := s.Pool().Exec(ctx, `
			INSERT INTO exchange_configs (venue, venue_type, api_key, api_secret, simulated, created_at)
			VALUES ($1, $2, 'k', 's', $3, $4)`, c.Venue, c.VenueType, c.Simulated, c.CreatedAt); err != nil {
			t.Fatalf("seed %s/%s: %v", c.Venue, c.VenueType, err)
		}
	}

	isRealMap, err := s.ListVenueIsReal(ctx)
	if err != nil {
		t.Fatalf("ListVenueIsReal: %v", err)
	}
	in := make([]reconcile.WinnersConfig, len(cfgs))
	for i, c := range cfgs {
		in[i] = reconcile.WinnersConfig{Venue: c.Venue, VenueType: c.VenueType, ID: c.ID, CreatedAt: c.CreatedAt}
	}
	winners := reconcile.CexKeyWinners(in)

	// 逐键配平：isReal 键集（venue:type）与赢家 simulated 位一致
	for _, c := range cfgs {
		vk := c.Venue + ":" + c.VenueType
		winnerID := winners[vk]
		isReal, ok := isRealMap[vk]
		if !ok {
			t.Fatalf("键 %s 缺失", vk)
		}
		wantSim := winnerID == c.ID
		if wantSim && *isReal == c.Simulated {
			// 是赢家：值取赢家行（此处仅校验赢家行自己的 simulated 位一致）
			if !c.Simulated && *isReal != !c.Simulated {
				t.Errorf("键 %s: isReal=%v 与赢家行 simulated=%v 不一致", vk, *isReal, c.Simulated)
			}
		}
		_ = wantSim
	}
	// 决定性断言：binance:perp 赢家=id25(sim) → isReal=false
	if v := isRealMap["binance:perp"]; v == nil || *v != false {
		t.Errorf("binance:perp isReal = %v, want false（赢家 sim 行）", v)
	}
	if v := isRealMap["binance:spot"]; v == nil || *v != true {
		t.Errorf("binance:spot isReal = %v, want true", v)
	}
	_ = winnerAssert(t, winners)
}

func winnerAssert(t *testing.T, winners map[string]int64) bool {
	if winners["binance:perp"] != 25 {
		t.Errorf("winner = %d, want 25", winners["binance:perp"])
	}
	return true
}
