// d060_cfd_board_test.go —— D-060①锚：纸面卡 CFD 结构行「当前」列改 join
// fin_carry_universe（mt×cex×canonical×structure 四元组），netNow=net_annual_pct
// 直出不再减 0.15 成本；缺行 fail-closed null；非 CFD 轨 join 回归不变。
package dashboard

import (
	"context"
	"testing"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

// TestD060_CfdRowNetNow CFD 行 join carry_universe → net_now=库值精确直出；
// 异 structure/异 venue 对不串键；缺行 null 传播。
func TestD060_CfdRowNetNow(t *testing.T) {
	now := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	fake := &fakePaperBoardSource{rows: []pgstore.PaperSpotCarry{
		// 目标 CFD 行：3001157×okx|XPD long_cfd_short_perp（业主实拍 60.19%/yr 在库）
		{ID: 1, VenueA: "3001157", VenueB: "okx", Symbol: "XPD", Side: "short_perp",
			Status: "active", QtyUSD: 1000, EntryFundingAnn: 60.19,
			EntryTs: now.Add(-48 * time.Hour), Structure: "long_cfd_short_perp"},
		// 同 venue 对异 structure（不串键——净额必异）
		{ID: 2, VenueA: "3001157", VenueB: "okx", Symbol: "XPD", Side: "long_perp",
			Status: "active", QtyUSD: 1000, EntryFundingAnn: -40.5,
			EntryTs: now.Add(-24 * time.Hour), Structure: "short_cfd_long_perp"},
		// CFD 行但 universe 缺 → null
		{ID: 3, VenueA: "39050818", VenueB: "okx", Symbol: "XAG", Side: "short_perp",
			Status: "active", QtyUSD: 1000, EntryFundingAnn: 30,
			EntryTs: now.Add(-12 * time.Hour), Structure: "long_cfd_short_perp"},
		// spot_carry 回归行（原 join 不动）
		{ID: 4, VenueA: "binance", Symbol: "ZUSDT", Side: "short_perp", Status: "active",
			QtyUSD: 1000, EntryFundingAnn: 25, EntryTs: now.Add(-24 * time.Hour), Structure: "spot_carry"},
	}}
	fake.cfdNets = map[string]float64{
		"3001157|okx|XPD|long_cfd_short_perp": 60.19,
		"3001157|okx|XPD|short_cfd_long_perp": -40.5,
	}
	fake.uni = map[string]pgstore.CexUniverseRow{
		"binance|ZUSDT": {Venue: "binance", Symbol: "ZUSDT", FundingAnn: boardF64(50.15)}, // |50.15|−0.15=50.00
	}
	resp, err := buildPaperSpotCarryBoard(context.Background(), fake, now)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	byID := map[int64]*v1.PaperSpotCarryRow{}
	for _, r := range resp.Rows {
		byID[r.Id] = r
	}
	r1 := byID[1]
	if r1.NetNow == nil || *r1.NetNow != 60.19 {
		t.Fatalf("CFD long 行 net_now = %v, want 60.19（net_annual_pct 直出不减成本）", r1.NetNow)
	}
	if r1.LastFundingAnn != nil {
		t.Fatalf("CFD 行不走 cex_universe join，last_funding_ann 应 null，got %v", *r1.LastFundingAnn)
	}
	if r2 := byID[2]; r2.NetNow == nil || *r2.NetNow != -40.5 {
		t.Fatalf("CFD short 行 net_now = %v, want -40.5（异 structure 键分离）", r2.NetNow)
	}
	if r3 := byID[3]; r3.NetNow != nil {
		t.Fatalf("CFD 缺 universe 行应 null，got %v", *r3.NetNow)
	}
	if r4 := byID[4]; r4.NetNow == nil || *r4.NetNow != 50.00 {
		t.Fatalf("spot_carry 回归 net_now = %v, want 50.00（原 join 不动）", r4.NetNow)
	}
}
