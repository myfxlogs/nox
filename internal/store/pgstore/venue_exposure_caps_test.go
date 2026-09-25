package pgstore

// D-475a S1/T 验收：fin_venue_exposure_caps upsert 幂等+list 回读+CHECK 负值拒；
// paper_spot_carry accrue_ticks/min_accrued_usd 列读写（S5 同源迁移）。

import (
	"context"
	"testing"
	"time"
)

func TestD475a_VenueExposureCaps(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_venue_exposure_caps`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	// upsert 幂等：同键二写更新 cap/note。
	if err := s.UpsertVenueExposureCap(ctx, &VenueExposureCap{VenueKey: "okx:perp", CapUSD: 5000, Enabled: true, Note: "a"}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if err := s.UpsertVenueExposureCap(ctx, &VenueExposureCap{VenueKey: "okx:perp", CapUSD: 0, Enabled: false, Note: "冻结"}); err != nil {
		t.Fatalf("upsert2: %v", err)
	}
	if err := s.UpsertVenueExposureCap(ctx, &VenueExposureCap{VenueKey: "3001157:mt5", CapUSD: 100, Enabled: true}); err != nil {
		t.Fatalf("upsert3: %v", err)
	}
	caps, err := s.ListVenueExposureCaps(ctx)
	if err != nil || len(caps) != 2 {
		t.Fatalf("list: %v len=%d", err, len(caps))
	}
	if c := caps["okx:perp"]; c.CapUSD != 0 || c.Enabled || c.Note != "冻结" {
		t.Fatalf("upsert 幂等失败: %+v", c)
	}
	// 负 cap → DB CHECK 拒。
	if err := s.UpsertVenueExposureCap(ctx, &VenueExposureCap{VenueKey: "x:y", CapUSD: -1, Enabled: true}); err == nil {
		t.Fatal("负 cap 应被 CHECK 拒")
	}
}

func TestD475a_PaperAccrueObsColumns(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `DELETE FROM paper_spot_carry WHERE venue = '3001157×okx'`); err != nil {
		t.Fatalf("clean: %v", err)
	}
	row := &PaperSpotCarry{
		Venue: "3001157×okx", Symbol: "XAUUSD", Structure: "long_cfd_short_perp",
		Side: "long", QtyUSD: 1000, EntryFundingAnn: 0.01,
	}
	if err := s.InsertPaperSpotCarry(ctx, row); err != nil {
		t.Fatalf("insert: %v", err)
	}
	to := time.Now()
	// 每轮 accruedAdd=1.0：第一轮 accrued=1，第二轮 accrued=2→min 应保持 1.0。
	if err := s.AccruePaperSpotCarry(ctx, row.ID, 0.01, 1.0, 0, 0, nil, &to, false); err != nil {
		t.Fatalf("accrue: %v", err)
	}
	got, err := s.GetPaperSpotCarryByID(ctx, row.ID)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.AccrueTicks != 1 || got.MinAccruedUSD == nil || *got.MinAccruedUSD != 1.0 {
		t.Fatalf("ticks=%d min=%v want 1/1.0", got.AccrueTicks, got.MinAccruedUSD)
	}
	if err := s.AccruePaperSpotCarry(ctx, row.ID, 0.01, 1.0, 0, 0, nil, &to, false); err != nil {
		t.Fatalf("accrue2: %v", err)
	}
	got, _ = s.GetPaperSpotCarryByID(ctx, row.ID)
	if got.AccrueTicks != 2 || *got.MinAccruedUSD != 1.0 || got.AccruedUSD != 2.0 {
		t.Fatalf("ticks=%d min=%v acc=%v want 2/1.0/2.0", got.AccrueTicks, *got.MinAccruedUSD, got.AccruedUSD)
	}
	// 缺席轮 accrueTo=nil：ticks 不涨（S4 缺席不计时同口径）。
	if err := s.AccruePaperSpotCarry(ctx, row.ID, 0.01, 0, 0, 1, nil, nil, false); err != nil {
		t.Fatalf("accrue-miss: %v", err)
	}
	got, _ = s.GetPaperSpotCarryByID(ctx, row.ID)
	if got.AccrueTicks != 2 {
		t.Fatalf("缺席轮 ticks 不应涨，got %d", got.AccrueTicks)
	}
}
