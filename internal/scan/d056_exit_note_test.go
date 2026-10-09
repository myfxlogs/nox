// d056_exit_note_test.go —— D-056④ 锚（真库全链驱动）：两轨退出后行 note
// 含统一判据串「exit=<reason> net=%.2f funding7d=%.2f swap=%.2f streak=%d」。
package scan

import (
	"context"
	"regexp"
	"testing"
	"time"

	"arb/internal/fact"
)

var exitNoteRe = regexp.MustCompile(`exit=(\w+) net=(-?\d+\.\d{2}) funding7d=(-?\d+\.\d{2}) swap=(-?\d+\.\d{2}) streak=(\d+)`)

// TestD056_ExitNoteCfd cfd 轨 adverse 退出 note 含当轮判定值
// （net=NetAnnual、funding7d=FundingAnn、swap=MT 腿 swap 年化、streak=low_streak）。
func TestD056_ExitNoteCfd(t *testing.T) {
	s := d350Store(t)
	ctx := context.Background()
	swapLong := 4.2
	row := UniverseRow{Canonical: "D056X", MtVenue: "277842155", CexVenue: "binance",
		NetAnnual: 10.95, FundingAnn: 11.10, SwapLongPct: &swapLong, Structure: "long_cfd_short_perp"}
	d144BookCover(t, s, "D056X", "277842155", "binance") // D-144 ③
	if opened, _, err := StepPaperCfdCarry(ctx, s, []UniverseRow{row}, time.Now(), d410Opts(), nil); err != nil || len(opened) != 1 {
		t.Fatalf("open: opened=%v err=%v", opened, err)
	}
	// 次轮同键 net 翻负 → adverse 当轮退。
	row.NetAnnual, row.FundingAnn = -3.0, -2.85
	if _, exited, err := StepPaperCfdCarry(ctx, s, []UniverseRow{row}, time.Now().Add(time.Hour), d410Opts(), nil); err != nil || exited != 1 {
		t.Fatalf("adverse: exited=%d err=%v", exited, err)
	}
	var note string
	if err := s.Pool().QueryRow(ctx,
		`SELECT note FROM paper_spot_carry WHERE symbol='D056X' ORDER BY id DESC LIMIT 1`).Scan(&note); err != nil {
		t.Fatalf("read note: %v", err)
	}
	m := exitNoteRe.FindStringSubmatch(note)
	if m == nil {
		t.Fatalf("note 缺退出判据串: %q", note)
	}
	if m[1] != "adverse" || m[2] != "-3.00" || m[3] != "-2.85" || m[4] != "4.20" {
		t.Fatalf("判据值不符（want exit=adverse net=-3.00 funding7d=-2.85 swap=4.20）: %q", m[0])
	}
}

// TestD056_ExitNoteCex 双 CEX 轨：normalized 退出（连续低率）note 含判定值
// + data_lost 缺席退出 note 同格式。
func TestD056_ExitNoteCex(t *testing.T) {
	s := d555Store(t)
	ctx := context.Background()
	t0 := time.Now().UTC()
	seedSettleFacts(t, s, "gate", "D56NUSDT", t0)
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: "gate", Symbol: "D56NUSDT", Value: 60, Ts: t0},
	}); err != nil {
		t.Fatalf("seed funding: %v", err)
	}
	cands := []CexPaperCand{d555s2Cand("D56NUSDT", "gate:spot", "gate:perp", 60, 100, 100)}
	if _, _, err := StepPaperCexCarry(ctx, s, cands, t0, d555Opts(), nil, nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 连续 2 轮低率（5<10 阈）→ normalized。
	cur := t0
	for i := 0; i < 2; i++ {
		cur = cur.Add(9 * time.Hour)
		seedSettleFacts(t, s, "gate", "D56NUSDT", cur)
		if err := s.InsertFacts(ctx, []fact.Fact{
			{Kind: fact.KindFunding, Venue: "gate", Symbol: "D56NUSDT", Value: 5, Ts: cur},
		}); err != nil {
			t.Fatalf("reseed: %v", err)
		}
		low := []CexPaperCand{d555s2Cand("D56NUSDT", "gate:spot", "gate:perp", 5, 100, 100)}
		if _, _, err := StepPaperCexCarry(ctx, s, low, cur, d555Opts(), nil, nil); err != nil {
			t.Fatalf("low round %d: %v", i, err)
		}
	}
	var note string
	if err := s.Pool().QueryRow(ctx,
		`SELECT note FROM paper_spot_carry WHERE symbol='D56NUSDT' AND exit_reason='normalized'
		 ORDER BY id DESC LIMIT 1`).Scan(&note); err != nil {
		t.Fatalf("read note: %v", err)
	}
	m := exitNoteRe.FindStringSubmatch(note)
	if m == nil || m[1] != "normalized" || m[2] != "5.00" || m[3] != "5.00" || m[5] != "2" {
		t.Fatalf("normalized 判据不符（want exit=normalized net=5.00 funding7d=5.00 streak=2）: %q", note)
	}
}
