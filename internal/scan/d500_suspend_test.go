// D-500 S1（验收 A1）：venue-silence suspend 双轨——venue 零产出行/dirty 行两路 suspend
// 冻结 miss_streak 不写 accrue，age_7d 墙钟出口仍在；venue 有产出缺席仍计 miss→data_lost。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

func d500CfdRow(id int64, entryTs time.Time) *pgstore.PaperSpotCarry {
	return &pgstore.PaperSpotCarry{
		ID: id, VenueA: "silentA", VenueB: "okx", Symbol: "XAU", Side: "short_perp",
		Structure: "long_cfd_short_perp", Status: "active", EntryTs: entryTs, QtyUSD: 1000,
	}
}

// TestD500_CfdSuspend_Silent venue 整轮零产出（休市）→ suspend：不计 miss_streak、不写 accrue。
func TestD500_CfdSuspend_Silent(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := newFakeMtmPaperStore()
	st.rows[1] = d500CfdRow(1, now.Add(-time.Hour))
	accrable, _ := st.ListAccrablePaperSpotCarry(ctx)
	exited, err := accrueExitCfdPaper(ctx, st, accrable,
		map[string]UniverseRow{}, map[string]string{}, map[string]bool{"silentA": true}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 0 || len(st.accrues) != 0 || len(st.exits) != 0 {
		t.Fatalf("suspend 应冻结: exited=%d accrues=%v exits=%v", exited, st.accrues, st.exits)
	}
	if st.rows[1].MissStreak != 0 {
		t.Fatalf("miss_streak 应冻结 0, got %d", st.rows[1].MissStreak)
	}
	// 对照：venue 有产出（非静默）但该配对缺席 → 真 miss 计 streak。
	exited, err = accrueExitCfdPaper(ctx, st, accrable,
		map[string]UniverseRow{}, map[string]string{}, map[string]bool{}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 0 || len(st.accrues) != 1 || st.rows[1].MissStreak != 1 {
		t.Fatalf("真 miss 应计 1: exited=%d accrues=%v miss=%d", exited, st.accrues, st.rows[1].MissStreak)
	}
}

// TestD500_CfdSuspend_Dirty 行在但被质量剔除（dirtyByKey）→ suspend 同冻结。
func TestD500_CfdSuspend_Dirty(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := newFakeMtmPaperStore()
	st.rows[1] = d500CfdRow(1, now.Add(-time.Hour))
	accrable, _ := st.ListAccrablePaperSpotCarry(ctx)
	key := "silentA×okx|XAU"
	exited, err := accrueExitCfdPaper(ctx, st, accrable,
		map[string]UniverseRow{}, map[string]string{key: "suspect"}, map[string]bool{}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 0 || len(st.accrues) != 0 || st.rows[1].MissStreak != 0 {
		t.Fatalf("dirty suspend 应冻结: exited=%d accrues=%v miss=%d", exited, st.accrues, st.rows[1].MissStreak)
	}
}

// TestD500_CfdSuspend_Age7d suspend 不豁免 age_7d——僵尸上限仍出口。
func TestD500_CfdSuspend_Age7d(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := newFakeMtmPaperStore()
	st.rows[1] = d500CfdRow(1, now.Add(-8*24*time.Hour))
	accrable, _ := st.ListAccrablePaperSpotCarry(ctx)
	exited, err := accrueExitCfdPaper(ctx, st, accrable,
		map[string]UniverseRow{}, map[string]string{}, map[string]bool{"silentA": true}, nil, now)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 1 || len(st.exits) != 1 || st.exits[0].reason != "age_7d" {
		t.Fatalf("want age_7d exit, got exited=%d exits=%v", exited, st.exits)
	}
}

// TestD500_CfdSuspend_Step 步进级接线路：venue 零产出行 → silentMt 装配 → suspend 生效。
func TestD500_CfdSuspend_Step(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := newFakeMtmPaperStore()
	st.rows[1] = d500CfdRow(1, now.Add(-time.Hour))
	// 三轮 venue 零产出——旧语义第三轮即 data_lost，suspend 后仍 active miss=0。
	for i := 0; i < paperMissExitRounds+1; i++ {
		if _, _, err := StepPaperCfdCarry(ctx, st, nil, now, PaperCfdOpts{}, nil); err != nil {
			t.Fatalf("round %d: %v", i, err)
		}
	}
	if st.rows[1].Status != "active" || st.rows[1].MissStreak != 0 {
		t.Fatalf("suspend 应保 active miss=0, got status=%s miss=%d", st.rows[1].Status, st.rows[1].MissStreak)
	}
}

// TestD500_MtmSuspend 双 MT 腿对称：任一腿 venue 静默 → suspend；双腿有产出腿缺 → 真 miss。
func TestD500_MtmSuspend(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	mk := func() (*fakeMtmPaperStore, []pgstore.PaperSpotCarry) {
		st := newFakeMtmPaperStore()
		st.rows[1] = &pgstore.PaperSpotCarry{
			ID: 1, VenueA: "silA", VenueB: "okB", Symbol: "XAU", Side: "long_a_short_b",
			Structure: "cfd_cfd", Status: "active", EntryTs: now.Add(-time.Hour), QtyUSD: 1000,
		}
		accrable, _ := st.ListAccrablePaperSpotCarry(ctx)
		return st, accrable
	}
	emptyBooks := map[string]mtSwap{}
	// ① venueA 静默 → suspend 冻结。
	st, accrable := mk()
	exited, err := accrueExitMtmPaper(ctx, st, accrable, emptyBooks, emptyBooks, map[string]bool{"silA": true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 0 || len(st.accrues) != 0 || st.rows[1].MissStreak != 0 {
		t.Fatalf("mtm suspend 应冻结: exited=%d accrues=%v miss=%d", exited, st.accrues, st.rows[1].MissStreak)
	}
	// ② 双 venue 有产出但本配对腿缺 → 真 miss 计 streak。
	st, accrable = mk()
	exited, err = accrueExitMtmPaper(ctx, st, accrable, emptyBooks, emptyBooks, map[string]bool{}, now)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 0 || len(st.accrues) != 1 || st.rows[1].MissStreak != 1 {
		t.Fatalf("mtm 真 miss 应计 1: exited=%d accrues=%v miss=%d", exited, st.accrues, st.rows[1].MissStreak)
	}
	// ③ suspend 期 age_7d 仍出口。
	st, accrable = mk()
	st.rows[1].EntryTs = now.Add(-8 * 24 * time.Hour)
	accrable[0].EntryTs = st.rows[1].EntryTs
	exited, err = accrueExitMtmPaper(ctx, st, accrable, emptyBooks, emptyBooks, map[string]bool{"okB": true}, now)
	if err != nil {
		t.Fatal(err)
	}
	if exited != 1 || len(st.exits) != 1 || st.exits[0].reason != "age_7d" {
		t.Fatalf("want mtm age_7d exit, got exited=%d exits=%v", exited, st.exits)
	}
}
