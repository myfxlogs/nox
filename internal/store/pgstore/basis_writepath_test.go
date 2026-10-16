package pgstore

// basis_writepath_test.go —— cfd paper basis 断供写径锚：last_basis_bps
// NULL 拍不擦 last-good（COALESCE 保列）；非空照常覆写。

import (
	"context"
	"testing"
	"time"
)

func TestAccrue_BasisNullKeepsLastGood(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE paper_spot_carry RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
	lb := -196.0
	row := &PaperSpotCarry{VenueA: "15032313", VenueB: "okx", Symbol: "TRXUSD",
		Structure: "short_cfd_long_perp", Side: "long_perp", QtyUSD: 1000,
		EntryFundingAnn: 0.05, EntryBasisBps: &lb}
	if err := s.InsertPaperSpotCarry(ctx, row); err != nil {
		t.Fatal(err)
	}
	id := row.ID
	to := time.Now()
	// 断供拍：lastBasisBps=nil → 列保 -196 不擦。
	if err := s.AccruePaperSpotCarry(ctx, id, 0, 0, 0, 1, nil, &to, false); err != nil {
		t.Fatal(err)
	}
	var got *float64
	if err := s.Pool().QueryRow(ctx, `SELECT last_basis_bps FROM paper_spot_carry WHERE id=$1`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != lb {
		t.Fatalf("NULL 拍应保 last-good -196, got %v", got)
	}
	// 供回拍：新值照常覆写。
	nv := -50.0
	if err := s.AccruePaperSpotCarry(ctx, id, 0, 0, 0, 0, &nv, &to, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Pool().QueryRow(ctx, `SELECT last_basis_bps FROM paper_spot_carry WHERE id=$1`, id).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != nv {
		t.Fatalf("供回拍应写新值 -50, got %v", got)
	}
}
