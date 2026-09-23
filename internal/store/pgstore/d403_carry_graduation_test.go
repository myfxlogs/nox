// d403_carry_graduation_test.go —— D-403 S6：store 层（DSN 门控）。
// ListPaperForGraduation 窗口（active 全收/exited 14d 裁剪）+InsertCarryGraduation 幂等+
// ListCfdCarryUniverseClean note 过滤（D-345 suspect 行拒入 hint 源）。
package pgstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func d403Store(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	prepareTestSchema(t, s)
	ctx := context.Background()
	for _, tbl := range []string{"carry_graduations", "paper_spot_carry", "fin_carry_universe"} {
		if _, err := s.pool.Exec(ctx, "TRUNCATE "+tbl+" CASCADE"); err != nil {
			t.Fatalf("truncate %s: %v", tbl, err)
		}
	}
	return s
}

func d403InsertPaper(t *testing.T, s *Store, venue, symbol, status string, structure string, exitReason string) int64 {
	t.Helper()
	r := &PaperSpotCarry{Venue: venue, Symbol: symbol, Structure: structure, AccruedUSD: 10}
	if err := s.InsertPaperSpotCarry(context.Background(), r); err != nil {
		t.Fatalf("insert paper: %v", err)
	}
	if status == "exited" {
		if err := s.ExitPaperSpotCarry(context.Background(), r.ID, exitReason); err != nil {
			t.Fatalf("exit paper: %v", err)
		}
	}
	return r.ID
}

func TestD403_ListPaperForGraduation_Window(t *testing.T) {
	s := d403Store(t)
	ctx := context.Background()
	activeID := d403InsertPaper(t, s, "v1×gate", "BTC", "active", "long_cfd_short_perp", "")
	recentExited := d403InsertPaper(t, s, "v2×gate", "ETH", "exited", "short_cfd_long_perp", "age_7d")
	oldExited := d403InsertPaper(t, s, "v3×gate", "XAU", "exited", "spot_carry", "normalized")
	// 20 天前退出=窗外
	if _, err := s.pool.Exec(ctx, `UPDATE paper_spot_carry SET exit_ts = now() - interval '20 days' WHERE id = $1`, oldExited); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListPaperForGraduation(ctx)
	if err != nil {
		t.Fatal(err)
	}
	ids := map[int64]bool{}
	for _, r := range rows {
		ids[r.ID] = true
	}
	if !ids[activeID] || !ids[recentExited] {
		t.Errorf("active+近窗 exited 应在集: %v", ids)
	}
	if ids[oldExited] {
		t.Errorf("20 天前 exited 应被 14d 窗裁剪: %v", ids)
	}
}

func TestD403_InsertCarryGraduation_Idempotent(t *testing.T) {
	s := d403Store(t)
	ctx := context.Background()
	paperID := d403InsertPaper(t, s, "v1×gate", "BTC", "active", "long_cfd_short_perp", "")
	mt := "1279967"
	g := &CarryGraduation{PaperID: paperID, Venue: "v1×gate", Symbol: "BTC", Structure: "long_cfd_short_perp", MtVenue: &mt, AgeDays: 6, AccruedUSD: 10}
	inserted, err := s.InsertCarryGraduation(ctx, g)
	if err != nil || !inserted {
		t.Fatalf("first insert inserted=%v err=%v", inserted, err)
	}
	inserted2, err := s.InsertCarryGraduation(ctx, &CarryGraduation{PaperID: paperID, Venue: "v1×gate", Symbol: "BTC", Structure: "long_cfd_short_perp", AgeDays: 7, AccruedUSD: 12})
	if err != nil {
		t.Fatal(err)
	}
	if inserted2 {
		t.Error("同 paper_id 二次插入应 DO NOTHING（幂等不重复建议）")
	}
	if g.ID == 0 {
		t.Error("首插应回填 id")
	}
	// 裁决落账 pending 守卫
	if err := s.UpdateCarryGraduationDecision(ctx, g.ID, "approved", nil, ""); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateCarryGraduationDecision(ctx, g.ID, "rejected", nil, ""); err == nil {
		t.Error("非 pending 再裁决应零行更新报错")
	}
	_, err = s.GetCarryGraduation(ctx, 999999)
	if !errors.Is(err, pgx.ErrNoRows) {
		t.Errorf("不存在 id 应 ErrNoRows, got %v", err)
	}
}

func TestD403_InsertXAssetCombo_UniqueViolation(t *testing.T) {
	s := d403Store(t)
	ctx := context.Background()
	r := &XAssetComboInsert{CanonicalBuy: "BTCUSDT", CanonicalSell: "BTCUSD", VenueBuy: "gate:perp", VenueSell: "1279967:mt5",
		MaxLongQty: 1, MaxShortQty: 1, MaxGrossUSD: 10, Simulated: false}
	if _, err := s.InsertXAssetAllowCombo(ctx, r); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InsertXAssetAllowCombo(ctx, r); !errors.Is(err, ErrXAssetComboExists) {
		t.Errorf("重复插入应 ErrXAssetComboExists, got %v", err)
	}
}

func TestD403_ListCfdCarryUniverseClean_NoteFilter(t *testing.T) {
	s := d403Store(t)
	ctx := context.Background()
	if err := s.ReplaceCarryUniverse(ctx, []CarryUniverseRow{
		{Canonical: "BTC", MtVenue: "1279967", CexVenue: "gate", Structure: "long_cfd_short_perp", Note: ""},
		{Canonical: "ETH", MtVenue: "1279967", CexVenue: "gate", Structure: "long_cfd_short_perp", Note: "suspect: swap 缺"},
		{Canonical: "XAU", MtVenue: "1279967", CexVenue: "gate", Structure: "spot_carry", Note: ""},
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := s.ListCfdCarryUniverseClean(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Canonical != "BTC" {
		t.Errorf("cfd+clean 应恰 1 行 BTC（suspect 拒入+spot_carry 非本集），got %+v", rows)
	}
}
