package pgstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// 自省模板四段锚（票面：seed 退出→reflection 模板四段断言）。
func TestPaperReflection_FourSegments(t *testing.T) {
	open := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	exit := open.Add(24 * time.Hour)
	fund, basis, fee, acc, conv, qty := 10.0, 2.5, 0.5, 0.8, -0.2, 5000.0
	txt := paperReflection(&paperReflectRow{
		Side: "short_perp", Structure: "spot_perp", ExitReason: "adverse",
		EntryFundingAnn: &fund, EntryBasisBps: &basis, EstRoundTripFeeUSD: &fee,
		AccruedUSD: &acc, ConvergenceUSD: &conv, QtyUSD: &qty,
		EntryTs: &open, ExitTs: &exit,
	})
	for _, seg := range []string{"入场假设:", "成本差:", "出场:", "模型偏差:"} {
		if !strings.Contains(txt, seg) {
			t.Fatalf("模板缺段 %s: %s", seg, txt)
		}
	}
	if !strings.Contains(txt, "出场: adverse") {
		t.Fatalf("exit_reason 未归因: %s", txt)
	}
	// 预期 carry=10%/365×1d×5000=1.3699 vs 实结=0.8-0.2=0.6——落库值直读非重建断言。
	if !strings.Contains(txt, "实结+0.6000USD") {
		t.Fatalf("实结值非落库直读: %s", txt)
	}
}

// 缺锚如实缺（不占位虚报——预期 NULL→段缺）。
func TestPaperReflection_MissingAnchors(t *testing.T) {
	txt := paperReflection(&paperReflectRow{Side: "x", ExitReason: "venue_starved"})
	if !strings.Contains(txt, "缺") || !strings.Contains(txt, "venue_starved") {
		t.Fatalf("缺锚未如实缺: %s", txt)
	}
}

// TestAnchorEntryExpectedCost_DualLeg 双腿聚合锚（refinement——旧径首腿 LATERAL
// 只锚单边 venue 费=跨所双腿仓 CEX+MT5 费率结构异系统性低估）：双腿仓锚=
// 两腿 cost_model 均值之和÷名义×1e4；单腿仓回归=旧口径同值；缺腿实测=如实
// 缺锚不记（禁估零）。指纹 hash 输入域=(combo,side,exit_reason) 不含本锚，
// 口径零动。
func TestAnchorEntryExpectedCost_DualLeg(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.pool.Exec(ctx, `TRUNCATE positions, position_legs, fin_cost_model RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() { _, _ = s.pool.Exec(ctx, `TRUNCATE positions, position_legs, fin_cost_model CASCADE`) })
	seed := func(t *testing.T, opp string, legs [][2]string, fees []float64, notional float64) int64 {
		t.Helper()
		var posID int64
		if err := s.pool.QueryRow(ctx,
			`INSERT INTO positions (opportunity_id, type, status, notional_usd) VALUES ($1,'XASSET_CARRY','OPEN',$2) RETURNING id`,
			opp, notional).Scan(&posID); err != nil {
			t.Fatalf("seed position: %v", err)
		}
		for i, lg := range legs {
			if _, err := s.pool.Exec(ctx,
				`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
				 VALUES ($1,$2,'OPEN',$3,$4,'SYM','BUY',1,100,'FILLED')`,
				posID, i+1, lg[0], lg[1]); err != nil {
				t.Fatalf("seed leg: %v", err)
			}
		}
		for i, fee := range fees {
			if _, err := s.pool.Exec(ctx,
				`INSERT INTO fin_cost_model (venue, venue_type, structure, bucket_hour, fills, fee_usd)
				 VALUES ($1,$2,'XASSET_CARRY',-1,4,$3)
				 ON CONFLICT (venue, venue_type, structure, bucket_hour)
				 DO UPDATE SET fills=4, fee_usd=EXCLUDED.fee_usd`,
				legs[i][0], legs[i][1], fee*4); err != nil {
				t.Fatalf("seed cost model: %v", err)
			}
		}
		return posID
	}

	// ① 双腿仓：binance(perp) 0.8 + 15032313(mt5) 1.2 → 均值和 2.0USD/笔。
	dual := seed(t, "XASSET_CARRY:TRXUSDT:a:b", [][2]string{{"binance", "perp"}, {"15032313", "mt5"}},
		[]float64{0.8, 1.2}, 4000)
	if err := s.AnchorEntryExpectedCost(ctx, dual); err != nil {
		t.Fatal(err)
	}
	var bps *float64
	if err := s.pool.QueryRow(ctx, `SELECT entry_expected_cost_bps FROM positions WHERE id=$1`, dual).Scan(&bps); err != nil {
		t.Fatal(err)
	}
	if bps == nil || abs64(*bps-5.0) > 1e-9 { // 2.0/4000×1e4=5.0
		t.Fatalf("双腿锚=两腿和 5.0bps 预期, got=%v", bps)
	}

	// ② 单腿仓回归：单腿单费 1.0/2000×1e4=5.0（旧口径同值）。
	single := seed(t, "XASSET_CARRY:SOLUSDT:c:d", [][2]string{{"binance", "perp"}}, []float64{1.0}, 2000)
	if err := s.AnchorEntryExpectedCost(ctx, single); err != nil {
		t.Fatal(err)
	}
	var singleBps *float64
	if err := s.pool.QueryRow(ctx, `SELECT entry_expected_cost_bps FROM positions WHERE id=$1`, single).Scan(&singleBps); err != nil {
		t.Fatal(err)
	}
	if singleBps == nil || abs64(*singleBps-5.0) > 1e-9 {
		t.Fatalf("单腿回归 5.0bps 预期, got=%v", singleBps)
	}

	// ③ 缺腿实测：MT5 腿 venue 无 cost_model 行（独立 venue 防与①行撞）→
	// 如实缺锚不记（NULL 保留，禁估零）。
	missing := seed(t, "XASSET_CARRY:XPTUSDT:e:f", [][2]string{{"binance", "perp"}, {"99993213", "mt5"}},
		[]float64{0.8}, 4000) // 第二腿无费行
	if err := s.AnchorEntryExpectedCost(ctx, missing); err != nil {
		t.Fatal(err)
	}
	var missingBps *float64
	if err := s.pool.QueryRow(ctx, `SELECT entry_expected_cost_bps FROM positions WHERE id=$1`, missing).Scan(&missingBps); err != nil {
		t.Fatal(err)
	}
	if missingBps != nil {
		t.Fatalf("缺腿实测应缺锚不记（NULL）, got=%v", *missingBps)
	}
}

func abs64(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
