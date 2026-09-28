// d548_slot_test.go —— D-548 验收锚：A1 cfd 槽 canonical 去重计数（6 账户×同 canonical
// 占 1 槽、异 canonical 各占）；A2 零收益僵尸槽三判据清理（全中→exit zero_accrual_stale，
// 任一不中→保留）。
package scan

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d548SeedActive 播一条 active cfd 纸面行（venue_a=MT 账户、venue_b=CEX、symbol=canonical）。
func d548SeedActive(t *testing.T, s *pgstore.Store, mtVenue, cexVenue, symbol, structure string, entryAgo time.Duration, accrued float64) {
	t.Helper()
	mv := mtVenue
	r := &pgstore.PaperSpotCarry{
		VenueA: mtVenue, VenueB: cexVenue, Symbol: symbol, Side: "short_perp",
		EntryTs: time.Now().Add(-entryAgo), QtyUSD: 100, AccruedUSD: accrued,
		Structure: structure, MtVenue: &mv,
	}
	if err := s.InsertPaperSpotCarry(context.Background(), r); err != nil {
		t.Fatalf("seed active %s@%s: %v", symbol, mtVenue, err)
	}
	// entry_ts 由 DEFAULT now() 落——直接 UPDATE 回拨入场龄（生产形态等值）。
	if _, err := s.Pool().Exec(context.Background(),
		`UPDATE paper_spot_carry SET entry_ts = now() - $1::double precision * interval '1 second',
		 accrued_usd = $4
		 WHERE venue_a = $2 AND symbol = $3 AND status = 'active'`,
		entryAgo.Seconds(), mtVenue, symbol, accrued); err != nil {
		t.Fatalf("backdate %s@%s: %v", symbol, mtVenue, err)
	}
}

// A1①：6 账户×同 canonical 合占 1 槽——其余 5 槽给异 canonical（第 7 个被挡）；
// 同 canonical 新账户行不占槽仍开（观察面每账户一份语义不变）。
func TestD548_A1_CanonicalSlotCount(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	// 生产病灶形：US30×6 账户全 active（accrued>0 防僵尸规则误伤本锚）。
	for i := 1; i <= 6; i++ {
		d548SeedActive(t, s, fmt.Sprintf("acct%d", i), "gate", "US30", "long_cfd_short_perp", 2*time.Hour, 1.0)
	}
	rows := []UniverseRow{}
	for i := 1; i <= 6; i++ {
		rows = append(rows, d374Row("US30", fmt.Sprintf("acct%d", i), "gate", "long_cfd_short_perp", 10, ""))
	}
	// 6 个异 canonical 候选（net 降序均可开）——槽额只剩 5。
	for i := 1; i <= 6; i++ {
		rows = append(rows, d374Row(fmt.Sprintf("NC%d", i), "acct1", "gate", "long_cfd_short_perp", 10, ""))
	}
	opened, _, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if len(opened) != 5 {
		t.Fatalf("A1①: US30 六行=1 槽→应再开 5 异 canonical, got opened=%v", opened)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 11 {
		t.Fatalf("A1①: active 应=6+5=11, got %d", len(active))
	}
	// A1②：同 canonical 新账户不占槽——槽满后 US30@acct7 仍开，NC7（新 canonical）被挡。
	rows2 := []UniverseRow{
		d374Row("US30", "acct7", "gate", "long_cfd_short_perp", 10, ""),
		d374Row("NC7", "acct1", "gate", "long_cfd_short_perp", 10, ""),
	}
	opened2, _, err := StepPaperCfdCarry(ctx, s, rows2, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step2: %v", err)
	}
	if len(opened2) != 1 || opened2[0] != "US30" {
		t.Fatalf("A1②: 同 canonical 复行应不占槽仍开, got opened=%v", opened2)
	}
	active, _ = s.ListActivePaperSpotCarry(ctx)
	us30 := 0
	for _, p := range active {
		if p.Symbol == "US30" {
			us30++
		}
	}
	if us30 != 7 || len(active) != 12 {
		t.Fatalf("A1②: US30 应 7 行/active 应 12, got us30=%d active=%d", us30, len(active))
	}
}

// A2：零收益僵尸三判据——accrued==0 ∧ age>24h ∧ 本轮净<带 → exit(zero_accrual_stale)+note；
// 三判据各留一反向锚：在带 net≥8 / age<24h / accrued>0 → 全保留。
func TestD548_A2_ZeroAccrualZombie(t *testing.T) {
	s := d410Store(t)
	ctx := context.Background()
	// 僵尸行：30h/accrued=0。
	d548SeedActive(t, s, "acctA", "gate", "US30", "long_cfd_short_perp", 30*time.Hour, 0)
	// 反向①：同条件但本轮 net 在带（20≥8）——不杀。
	d548SeedActive(t, s, "acctB", "gate", "US30B", "long_cfd_short_perp", 30*time.Hour, 0)
	// 反向②：accrued>0（已产出非零收益）——不杀。
	d548SeedActive(t, s, "acctC", "gate", "US30C", "long_cfd_short_perp", 30*time.Hour, 0.5)
	// 反向③：age<24h——不杀。
	d548SeedActive(t, s, "acctD", "gate", "US30D", "long_cfd_short_perp", 5*time.Hour, 0)
	rows := []UniverseRow{
		d374Row("US30", "acctA", "gate", "long_cfd_short_perp", 7, ""),   // net<8
		d374Row("US30B", "acctB", "gate", "long_cfd_short_perp", 20, ""), // net 在带
		d374Row("US30C", "acctC", "gate", "long_cfd_short_perp", 7, ""),
		d374Row("US30D", "acctD", "gate", "long_cfd_short_perp", 7, ""),
	}
	_, exited, err := StepPaperCfdCarry(ctx, s, rows, time.Now(), d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	if exited != 1 {
		t.Fatalf("A2: 三判据全中恰 1 行退, got exited=%d", exited)
	}
	var reason, note string
	err = s.Pool().QueryRow(ctx, `SELECT exit_reason, note FROM paper_spot_carry WHERE symbol='US30' AND venue_a='acctA'`).Scan(&reason, &note)
	if err != nil {
		t.Fatalf("read exit: %v", err)
	}
	if reason != "zero_accrual_stale" {
		t.Fatalf("A2: reason=%q want zero_accrual_stale", reason)
	}
	if !strings.Contains(note, "accrued") || !strings.Contains(note, "net=") {
		t.Fatalf("A2: note 应记三判据值, got %q", note)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	if len(active) != 3 {
		t.Fatalf("A2: 反向三行应全保留, got %d", len(active))
	}
}
