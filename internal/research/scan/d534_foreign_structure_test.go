// d534_foreign_structure_test.go —— D-534：①spot 轨异结构行零接触（data_lost 互杀病灶
// 实证：58 行退出全带本轨 note=唯一写手）；②cfd 轨 entry 锚补写（簿后生覆盖→首测值补锚）。
package scan

import (
	"context"
	"testing"
	"time"

	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

func d534Store(t *testing.T) *pgstore.Store {
	t.Helper()
	s := storetest.SharedStore(t, "NOX_TEST_PG_DSN")
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE fin_carry_universe, fin_cex_universe, paper_spot_carry, carry_graduations,
		 fin_xasset_monitor RESTART IDENTITY`); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	if _, err := s.Pool().Exec(context.Background(),
		`DELETE FROM exchange_configs WHERE venue='d534okx'`); err != nil {
		t.Fatalf("cfg clean: %v", err)
	}
	return s
}

// TestD534_SpotForeignStructureNoKill 异结构行（perp_delivery/perp_perp/cfd/cex/mtm）
// 在 spot 轨缺席universe时不得计 miss、不得退出——D-532 data_lost 58 行归因实证。
func TestD534_SpotForeignStructureNoKill(t *testing.T) {
	s := d534Store(t)
	ctx := context.Background()
	for _, st := range []struct{ venueA, venueB, sym, structure string }{
		{"binance", "binance", "BTCUSDT_261225", "perp_delivery"},
		{"okx", "binance", "ZAMA", "perp_perp"},
		{"3001157", "gate", "NAS100", "short_cfd_long_perp"},
		{"gate", "gate", "AAA", "long_spot_short_perp"},
		{"3001157", "15032313", "XAUUSD", "cfd_cfd"},
	} {
		if err := s.InsertPaperSpotCarry(ctx, &pgstore.PaperSpotCarry{
			VenueA: st.venueA, VenueB: st.venueB, Symbol: st.sym, Side: "short_perp",
			EntryFundingAnn: 30, QtyUSD: 1000, Structure: st.structure,
		}); err != nil {
			t.Fatalf("seed %s: %v", st.structure, err)
		}
	}
	// 四轮缺席（universe 空）——修复前第 3 轮即 data_lost 误杀。
	for i := 0; i < 4; i++ {
		if _, _, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), time.Now(), nil); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	var alive, miss int
	if err := s.Pool().QueryRow(ctx,
		`SELECT count(*), COALESCE(sum(miss_streak),0) FROM paper_spot_carry WHERE status='active'`).Scan(&alive, &miss); err != nil {
		t.Fatalf("count: %v", err)
	}
	if alive != 5 || miss != 0 {
		t.Fatalf("异结构行被 spot 轨触碰: alive=%d miss=%d (want 5/0)", alive, miss)
	}
	// 正控：spot_carry 行缺席仍正常计 miss→data_lost。
	if err := s.InsertPaperSpotCarry(ctx, &pgstore.PaperSpotCarry{
		VenueA: "binance", Symbol: "SPOTX", Side: "short_perp",
		EntryFundingAnn: 30, QtyUSD: 1000, Structure: "spot_carry",
	}); err != nil {
		t.Fatalf("seed spot row: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, _, err := StepPaperSpotCarry(ctx, s, NewEntryStreak(), time.Now(), nil); err != nil {
			t.Fatalf("spot round %d: %v", i, err)
		}
	}
	var reason string
	if err := s.Pool().QueryRow(ctx,
		`SELECT COALESCE(exit_reason,'') FROM paper_spot_carry WHERE symbol='SPOTX'`).Scan(&reason); err != nil {
		t.Fatalf("read: %v", err)
	}
	if reason != "data_lost" {
		t.Fatalf("spot 行缺席应 data_lost: reason=%q", reason)
	}
}

// TestD534_CfdEntryAnchorBackfill entry_basis NULL 行（簿后生覆盖）在 accrue 轮
// 簿已有时补锚+补 est_fee（锚值=实测首行，非臆造）。
func TestD534_CfdEntryAnchorBackfill(t *testing.T) {
	s := d534Store(t)
	ctx := context.Background()
	now := time.Now()
	// 簿行：pair=JP225USDT-JP225 okx:perp×3001157:mt5，noise 非 NULL（未成噪行 gate 不放行）。
	noise := 5.0
	if err := s.InsertXassetMonitor(ctx, &pgstore.XassetMonitor{
		Pair: "JP225USDT-JP225", EntrySpreadBps: 28.5, NoiseBps: &noise,
		CexVenue: "d534okx:perp", MtVenue: "3001157:mt5",
	}); err != nil {
		t.Fatalf("seed monitor: %v", err)
	}
	// 费率源（est_fee 补写依赖）。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO exchange_configs (venue, venue_type, taker_fee_bps, api_key, api_secret) VALUES ('d534okx','perp',5.0,'x','x')`); err != nil {
		t.Fatalf("seed fee: %v", err)
	}
	// 纸面行：entry_basis NULL（簿后生入场场景）。
	fee0 := 0.0
	_ = fee0
	if err := s.InsertPaperSpotCarry(ctx, &pgstore.PaperSpotCarry{
		VenueA: "3001157", VenueB: "d534okx", Symbol: "JP225", Side: "long_perp",
		EntryFundingAnn: 12, QtyUSD: 1000, Structure: "long_cfd_short_perp",
	}); err != nil {
		t.Fatalf("seed paper: %v", err)
	}
	accrable, err := s.ListAccrablePaperSpotCarry(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if _, err := accrueExitCfdPaper(ctx, s, accrable, map[string]UniverseRow{}, map[string]string{}, map[string]bool{}, newAssetClassView(nil), now); err != nil {
		t.Fatalf("accrue: %v", err)
	}
	var basis, fee *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT entry_basis_bps, est_round_trip_fee_usd FROM paper_spot_carry WHERE symbol='JP225'`).Scan(&basis, &fee); err != nil {
		t.Fatalf("read: %v", err)
	}
	if basis == nil || *basis != 28.5 {
		t.Fatalf("entry 锚未补: %v", basis)
	}
	// est_fee=d534okx 往返（5bps×2×$1000/1e4=$1）+簿 spread 往返（28.5×2×1000/1e4=$5.7）=$6.7
	if fee == nil || *fee < 6.6 || *fee > 6.8 {
		t.Fatalf("est_fee 未补/错: %v", fee)
	}
}
