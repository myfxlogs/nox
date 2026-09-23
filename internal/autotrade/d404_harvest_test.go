package autotrade

// D-404 S2：harvest 收割判定真腿化——冻结锚 sign 公式 + 尺度平移（line=|anchor|−capture，
// capture=开仓时点 monitor entry_spread−floor 实测），要素缺回退 monitor 代理路径。
import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// d404XAssetPos OPEN XASSET 仓 + 双腿 FILLED + 冻结锚两列 UPDATE 落库。
// anchor/floor 传 nil = NULL（回退路径用例）。ref=PriceActual 4000（basis 换算基准）。
func d404XAssetPos(t *testing.T, s *pgstore.Store, anchor, floor *float64) *pgstore.Position {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	oppID := "XASSET_CARRY:XPTUSD:3001157:mt5:okx:perp"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "XPTUSD", CanonicalSell: d341strptr("XPTUSDT"),
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp", ExpiresAt: now.Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opp: %v", err)
	}
	posID, err := s.CreatePosition(ctx, &pgstore.Position{OpportunityID: oppID, Type: "XASSET_CARRY", Status: "OPEN"})
	if err != nil {
		t.Fatalf("create pos: %v", err)
	}
	pa := 4000.0
	for _, l := range []pgstore.PositionLeg{
		{PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XPTUSD", Side: "BUY", Qty: 1, Status: "FILLED", PriceActual: &pa},
		{PositionID: posID, LegNo: 2, Phase: "OPEN", Venue: "okx:perp", Symbol: "XPT-USDT-SWAP", Side: "SELL", Qty: 1, Status: "FILLED"},
	} {
		if err := s.CreatePositionLeg(ctx, &l); err != nil {
			t.Fatalf("create leg: %v", err)
		}
	}
	if _, err := s.Pool().Exec(ctx,
		`UPDATE positions SET expected_entry_spread_bps=$1, entry_harvest_floor_bps=$2 WHERE id=$3`,
		anchor, floor, posID); err != nil {
		t.Fatalf("update anchors: %v", err)
	}
	// GetPosition 列不含 entry_noise/entry_harvest_floor——评估链读口=ListPositions
	// （scanPositions 含两列，与生产 scanAndClose 同路径），须同口径取回。
	rows, err := s.ListPositions(ctx, "OPEN", 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list pos: rows=%d err=%v", len(rows), err)
	}
	return &rows[0]
}

// d404Quotes 双腿报价：basis=(buyBid−sellAsk)/4000×1e4。age<0 = 超龄报价。
func d404Quotes(now time.Time, buyBid, sellAsk float64, age time.Duration) *bus.QuoteBus {
	qb := bus.New()
	ts := now.Add(-age)
	qb.Publish(bus.Quote{Symbol: "XPTUSD", Venue: "3001157", VenueType: "mt5", Bid: buyBid, Ask: buyBid + 1, Time: ts, VenueSymbol: "XPTUSD"})
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "okx", VenueType: "perp", Bid: sellAsk - 1, Ask: sellAsk, Time: ts, VenueSymbol: "XPT-USDT-SWAP"})
	return qb
}

func d404Engine(s *pgstore.Store, qb *bus.QuoteBus) *Engine {
	return &Engine{store: s, quotes: qb, fundingBreach: map[int64]int{}, cfg: Config{XAssetQuoteMaxAge: 5 * time.Minute}}
}

// TestD404_HarvestRealLeg 真腿尺度矩阵（①②④⑧a 共用锚：anchor=−12.39/floor=4.73/
// 开仓时点 monitor entry=7.73 → capture=3 → line=|−12.39|−3=9.39，触发条件 basis≥−9.39）。
func TestD404_HarvestRealLeg(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name          string
		buyBid        float64 // sellAsk 恒 4000 → basis=(buyBid−4000)/4000×1e4
		wantTriggered bool
	}{
		{"①负锚收敛到线 basis=-9.0", 3996.4, true},
		{"②负锚过冲翻正 basis=+2.0", 4000.8, true},
		{"④未收敛 basis=-11.0", 3995.6, false},
		{"⑧尺度平移 basis=-8.0（裸 floor 线 -4.73 不触发，平移线 -9.39 触发）", 3996.8, true},
	}
	for _, tc := range cases {
		s := testAccumStore(t)
		d311Truncate(t, s)
		p := d404XAssetPos(t, s, d341fptr(-12.39), d341fptr(4.73))
		d341SeedMonitorRow(t, s, d341fptr(7.73), d341fptr(9), d341fptr(4.73))
		e := d404Engine(s, d404Quotes(time.Now().UTC(), tc.buyBid, 4000, 0))
		close, reason := e.harvestCheck(ctx, p)
		if close != tc.wantTriggered {
			t.Errorf("%s: close=%v want=%v reason=%q", tc.name, close, tc.wantTriggered, reason)
		}
		if close && !containsSub(reason, "harvest") {
			t.Errorf("%s: reason 缺 harvest 前缀 %q", tc.name, reason)
		}
	}
}

// TestD404_HarvestPositiveAnchor ③正锚对称：anchor=+12.39 → sign=+1，line=9.39，
// basis≤+9.39 触发（倒挂入场收敛方向=basis 下行）。
func TestD404_HarvestPositiveAnchor(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d404XAssetPos(t, s, d341fptr(12.39), d341fptr(4.73))
	d341SeedMonitorRow(t, s, d341fptr(7.73), d341fptr(9), d341fptr(4.73))
	e := d404Engine(s, d404Quotes(time.Now().UTC(), 4003.6, 4000, 0)) // basis=+9.0
	if close, reason := e.harvestCheck(ctx, p); !close || !containsSub(reason, "harvest") {
		t.Errorf("正锚 basis=+9.0 ≤ line 应触发, reason=%q", reason)
	}
	e2 := d404Engine(s, d404Quotes(time.Now().UTC(), 4004.0, 4000, 0)) // basis=+10.0
	if close, _ := e2.harvestCheck(ctx, p); close {
		t.Error("正锚 basis=+10 > line=9.39 不应触发")
	}
}

// TestD404_HarvestFallback floor NULL / 开仓时点 monitor 行超窗 → 回退代理路径行为不变。
func TestD404_HarvestFallback(t *testing.T) {
	ctx := context.Background()
	// floor NULL → 回退：monitor entry=5 ≤ floor=9 → 触发（d341 语义保留）
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d404XAssetPos(t, s, d341fptr(-12.39), nil)
	d341SeedMonitorRow(t, s, d341fptr(5), d341fptr(9), d341fptr(9))
	e := d404Engine(s, d404Quotes(time.Now().UTC(), 3996.4, 4000, 0))
	if close, reason := e.harvestCheck(ctx, p); !close || !containsSub(reason, "harvest") {
		t.Errorf("floor NULL 回退 monitor entry≤floor 应触发, reason=%q", reason)
	}
	// 开仓时点 monitor 行超窗（opened_at=2h 前 vs 行 ts=now）→ 回退；
	// 回退后 entry=20>floor=9 → 不触发
	s2 := testAccumStore(t)
	d311Truncate(t, s2)
	p2 := d404XAssetPos(t, s2, d341fptr(-12.39), d341fptr(4.73))
	if _, err := s2.Pool().Exec(ctx, `UPDATE positions SET opened_at=now()-interval '2 hours' WHERE id=$1`, p2.ID); err != nil {
		t.Fatalf("update opened_at: %v", err)
	}
	rows2, _ := s2.ListPositions(ctx, "OPEN", 10, 0)
	p2 = &rows2[0]
	d341SeedMonitorRow(t, s2, d341fptr(20), d341fptr(9), d341fptr(9))
	e2 := d404Engine(s2, d404Quotes(time.Now().UTC(), 3996.4, 4000, 0))
	if close, reason := e2.harvestCheck(ctx, p2); close {
		t.Errorf("entryRow 超窗回退后 entry>floor 不应触发, reason=%q", reason)
	}
	// 超窗回退 + entry≤floor → 触发（证明回退路径活着）
	s3 := testAccumStore(t)
	d311Truncate(t, s3)
	p3 := d404XAssetPos(t, s3, d341fptr(-12.39), d341fptr(4.73))
	if _, err := s3.Pool().Exec(ctx, `UPDATE positions SET opened_at=now()-interval '2 hours' WHERE id=$1`, p3.ID); err != nil {
		t.Fatalf("update opened_at: %v", err)
	}
	rows3, _ := s3.ListPositions(ctx, "OPEN", 10, 0)
	p3 = &rows3[0]
	d341SeedMonitorRow(t, s3, d341fptr(5), d341fptr(9), d341fptr(9))
	e3 := d404Engine(s3, d404Quotes(time.Now().UTC(), 3996.4, 4000, 0))
	if close, reason := e3.harvestCheck(ctx, p3); !close || !containsSub(reason, "harvest") {
		t.Errorf("entryRow 超窗回退后 entry≤floor 应触发, reason=%q", reason)
	}
}

// TestD404_HarvestQuoteGate ⑥缺报价不触发 / ⑦报价超龄不触发（真腿要素齐全下 fail-closed）。
func TestD404_HarvestQuoteGate(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d404XAssetPos(t, s, d341fptr(-12.39), d341fptr(4.73))
	d341SeedMonitorRow(t, s, d341fptr(7.73), d341fptr(9), d341fptr(4.73))
	e := d404Engine(s, bus.New()) // 空 qbus=缺报价
	if close, reason := e.harvestCheck(ctx, p); close {
		t.Errorf("缺报价不应触发, reason=%q", reason)
	}
	e2 := d404Engine(s, d404Quotes(time.Now().UTC(), 3996.4, 4000, 10*time.Minute)) // age=10min>5min
	if close, reason := e2.harvestCheck(ctx, p); close {
		t.Errorf("报价超龄不应触发, reason=%q", reason)
	}
}
