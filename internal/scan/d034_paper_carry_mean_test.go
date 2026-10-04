// d034_paper_carry_mean_test.go —— D-034 验收锚：carry_universe funding 改 facts
// 7d 滚动均值年化（fact.FundingMeanAnnual 共享单实现）；瞬时值仅留展示列。
// 场景=周六瞬时 funding=0 但 7d 均值 28.6 → long 结构 net>8 可开、不 adverse。
package scan

import (
	"context"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// TestD034_FundingMeanSaturdayOpen 瞬时=0 + 窗内均值 28.6：
// ① universe 行 FundingAnn=均值（判定/落表口径）、FundingInst=0（瞬时展示列）；
// ② long_cfd_short_perp net=均值+swap(0)>8 → top-N 开纸面仓（瞬时=0 不误杀）；
// ③ entry_funding_ann 落瞬时值（展示列语义）。
func TestD034_FundingMeanSaturdayOpen(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	facts := []fact.Fact{
		// 周六瞬时=0（最新样本 ts=now）——旧口径下 net=0 不可开/adverse 误杀形态。
		{Kind: "funding", Venue: "okx", Symbol: "XAUUSDT", Value: 0, Ts: now},
		// 窗内 21×8h 样本 30%/yr → 均值 (630+0)/22 ≈ 28.64（含周末零样本=诚实日均）。
		{Kind: "swap_long", Venue: "3001157", Symbol: "XAUUSDm", Value: 0, Ts: now},
	}
	for i := 1; i <= 21; i++ {
		facts = append(facts, fact.Fact{
			Kind: "funding", Venue: "okx", Symbol: "XAUUSDT", Value: 30,
			Ts: now.Add(-time.Duration(i) * 8 * time.Hour),
		})
	}
	sc := NewCarryUniverseScanner(&fakeScanStore{
		facts: facts,
		mappings: []pgstore.SymbolMapping{
			{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "XAUUSD", IsPrimary: true},
		},
	})
	res, err := sc.Run(ctx, now.Add(-24*time.Hour))
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	var row *UniverseRow
	for i := range res.Rows {
		if r := &res.Rows[i]; r.Structure == "long_cfd_short_perp" && r.Canonical == "XAU" && r.CexVenue == "okx" {
			row = r
		}
	}
	if row == nil {
		t.Fatalf("long 行未产出: %+v", res.Rows)
	}
	// 均值口径：net=28.64+swap(0)>8 且 FundingInst=0 瞬时保留
	if row.NetAnnual <= PaperCfdOpenNetPct || row.NetAnnual > 40 {
		t.Fatalf("net=%.3f 应为 7d 均值口径（≈28.6），非瞬时 0", row.NetAnnual)
	}
	if row.FundingAnn != row.NetAnnual || row.FundingInst != 0 {
		t.Fatalf("FundingAnn=%.3f（均值）/FundingInst=%.3f（瞬时）错", row.FundingAnn, row.FundingInst)
	}
	// ② 开仓：瞬时=0 行不误杀——net≥8 top-N 可开
	s := d410Store(t)
	opened, _, err := StepPaperCfdCarry(ctx, s, res.Rows, now, d410Opts(), nil)
	if err != nil {
		t.Fatalf("step: %v", err)
	}
	active, _ := s.ListActivePaperSpotCarry(ctx)
	var got *pgstore.PaperSpotCarry
	for i := range active {
		if a := &active[i]; a.Symbol == "XAU" && a.Structure == "long_cfd_short_perp" {
			got = a
		}
	}
	if got == nil || len(opened) == 0 {
		t.Fatalf("均值 net>8 应开 long 纸面仓，active=%v opened=%v", active, opened)
	}
	// ③ 展示列语义：entry_funding_ann=入场闸判定值 NetAnnual（D-060② 承诺口径——
	// 瞬时值周末落 0 致面板承诺列恒「—」，D-034 展示列口径被 D-060 裁定覆盖）。
	if got.EntryFundingAnn != row.NetAnnual {
		t.Fatalf("entry_funding_ann=%v want %.3f（入场闸判定值=净 carry 7d 均值）", got.EntryFundingAnn, row.NetAnnual)
	}
	// ④ 不 adverse：同轮行再判（net>0 → 不退、不 zero_accrual）
	accrable, _ := s.ListAccrablePaperSpotCarry(ctx)
	rowByKey := map[string]UniverseRow{row.MtVenue + "×" + row.CexVenue + "|" + row.Canonical + "|" + row.Structure: *row}
	exited, err := accrueExitCfdPaper(ctx, s, accrable, rowByKey, map[string]string{}, map[string]bool{}, nil, now)
	if err != nil {
		t.Fatalf("accrue: %v", err)
	}
	if exited != 0 {
		t.Fatalf("均值 net>0 不得 adverse/zero_accrual 退出，exited=%d", exited)
	}
}
