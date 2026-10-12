// d051_r1_round_test.go —— D-051 R1④锚：hook/轮 pass 解耦（真库，复用 d051 夹具
// D51% 前缀域隔离）——
// ① 同秒 40 次 Open 调用 → 新开行 miss_streak 恒 0（Open 不触计息/缺席计数）；
// ② unfiltered 候选开仓后下一轮 AccrueExit 命中（非缺席——last_funding_ann 落库、miss=0）；
// ③ miss_streak 按轮计——缺席一轮 +1，满 paperMissExitRounds 轮才 data_lost（非按调用数）。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// d051r1Row 目标行 miss_streak/last_basis_bps/status 直读。
func d051r1Row(t *testing.T, s *pgstore.Store, canonical string) (miss int, basis *float64, status string) {
	t.Helper()
	err := s.Pool().QueryRow(context.Background(),
		`SELECT miss_streak,last_basis_bps,status FROM paper_spot_carry WHERE symbol=$1 AND structure='long_spot_short_perp'`,
		canonical).Scan(&miss, &basis, &status)
	if err != nil {
		t.Fatalf("read row %s: %v", canonical, err)
	}
	return miss, basis, status
}

// TestD051R1_OpenBurstZeroMiss 同秒 40 次 hook 调用（每调一 Open）→ 新开行
// miss_streak 恒 0——Open 段不触计息/缺席判定（R1① 解耦钉：旧合成签名 40 调
// 会把缺席计数打进 miss_streak 致 data_lost）。
func TestD051R1_OpenBurstZeroMiss(t *testing.T) {
	s := d051Store(t)
	d051SeedDepth(t, s, "okx", "D51R", 8000)
	d051SeedFunding(t, s, "okx", "D51RUSDT", 15.0)
	cand := d555Cand("D51RUSDT", "okx:spot", "okx:perp", 12.0)
	ctx, now := context.Background(), time.Now()
	for i := 0; i < 40; i++ {
		opened, skipN, err := StepPaperCexCarryOpen(ctx, s, []CexPaperCand{cand}, now, d051Opts(&PaperSkipCounter{}), nil, nil)
		if err != nil {
			t.Fatalf("open #%d: %v", i, err)
		}
		if i == 0 && len(opened) != 1 {
			t.Fatalf("首调应开 1 行，opened=%v skips=%v", opened, skipN)
		}
		if i > 0 && len(opened) != 0 {
			t.Fatalf("在仓去重后应零开，opened=%v", opened)
		}
	}
	miss, _, status := d051r1Row(t, s, "D51RUSDT")
	if miss != 0 || status != "active" {
		t.Fatalf("40 次 Open 后 miss_streak 应 0 且 active，got miss=%d status=%s", miss, status)
	}
}

// TestD051R1_AccrueHitAndMissPerRound unfiltered 候选开仓→下一轮 AccrueExit
// 吃同流快照命中（last_funding_ann 落库、miss=0）；候选缺席按轮 +1，
// 满 paperMissExitRounds 轮才 data_lost。
func TestD051R1_AccrueHitAndMissPerRound(t *testing.T) {
	s := d051Store(t)
	d051SeedDepth(t, s, "okx", "D51S", 8000)
	d051SeedFunding(t, s, "okx", "D51SUSDT", 15.0)
	cand := d555Cand("D51SUSDT", "okx:spot", "okx:perp", 12.0)
	ctx, now := context.Background(), time.Now()
	if _, _, err := StepPaperCexCarryOpen(ctx, s, []CexPaperCand{cand}, now, d051Opts(&PaperSkipCounter{}), nil, nil); err != nil {
		t.Fatalf("open: %v", err)
	}
	// 下一轮 pass：同 unfiltered 集 → 命中（非缺席）——miss=0 且 last_basis_bps 落库
	// （开仓 cand 无报价 basis NULL；accrue cand 带报价→落值=候选命中铁证，
	// 缺席轮则 miss+1 且 basis 诚实缺 NULL）。
	candQ := cand
	candQ.SpotAsk, candQ.PerpBid = 100, 100.05 // basis=+5bps
	if _, _, err := StepPaperCexCarryAccrueExit(ctx, s, []CexPaperCand{candQ}, now.Add(30*time.Second)); err != nil {
		t.Fatalf("round pass: %v", err)
	}
	miss, basis, status := d051r1Row(t, s, "D51SUSDT")
	if miss != 0 || status != "active" {
		t.Fatalf("命中轮应 miss=0 active，got miss=%d status=%s", miss, status)
	}
	if basis == nil || *basis < 4.9 || *basis > 5.1 {
		t.Fatalf("命中轮应落 last_basis_bps≈5，got %v", basis)
	}
	// 缺席按轮计：每空轮 +1，末轮 data_lost（hook 子集缺席不再误计——调用数无关）。
	for i := 1; i <= paperMissExitRounds; i++ {
		if _, _, err := StepPaperCexCarryAccrueExit(ctx, s, nil, now.Add(30*time.Second*time.Duration(i+1))); err != nil {
			t.Fatalf("miss round %d: %v", i, err)
		}
		miss, _, status = d051r1Row(t, s, "D51SUSDT")
		if i < paperMissExitRounds {
			if miss != i || status != "active" {
				t.Fatalf("缺席第 %d 轮应 miss=%d active，got miss=%d status=%s", i, i, miss, status)
			}
		} else if status != "exited" {
			t.Fatalf("缺席满 %d 轮应 data_lost 退出，got miss=%d status=%s", paperMissExitRounds, miss, status)
		}
	}
}
