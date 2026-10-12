package autotrade

// harvestCheck 改 econ.NetIfClosedNow 口径——#505 形态回放锚：
//   carry 20.8%/yr=$2.37/天，入场费 $2.08 + 退出估 $2.60 → 回本 ≈2.0d ≤3.0 放行；
//   开仓 2min NetIfClosedNow≈−2.9<0 永不收割；carry 11.6%/yr → 回本 3.6d>3.0 拒开。
// 旧收割线（anchor/floor/monitor 行）语义已退役——本文件整组重写为 econ 判定矩阵。
import (
	"context"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// d014StubSvc 只钉 econ 需要的 USDPerLotForLeg（qty=1 腿 → 腿名义 $4160，#505 $4160/0.01手 同款）。
type d014StubSvc struct{ engineService }

func (d014StubSvc) USDPerLotForLeg(canonical, venueKey, side string) (float64, bool) {
	return 4160, true
}

// d014FeeFn D-014 费率源 fake：okx:perp=5bps（实拍）；MT 腿 0（3001157 无佣金）；
// unknown venue → false（fail-closed 分支用例）。
func d014FeeFn(ok bool) func(context.Context, string) (float64, bool) {
	return func(_ context.Context, venueKey string) (float64, bool) {
		if !ok {
			return 0, false
		}
		switch venueKey {
		case "okx:perp":
			return 5.0, true
		case "3001157:mt5":
			return 0.0, true
		}
		return 0, false
	}
}

// d014EconEngine harvest 专用 engine（fundingBreach map + cfg 最小集）。
func d014EconEngine(s *pgstore.Store, qb *bus.QuoteBus, feeOK bool) *Engine {
	return &Engine{
		store: s, service: d014StubSvc{}, quotes: qb, fundingBreach: map[int64]int{},
		cfg: gate.Config{
			XAssetQuoteMaxAge:  5 * time.Minute,
			SwapFactMaxAge:     2 * time.Hour,
			EconFeeBps:         d014FeeFn(feeOK),
			HarvestHorizonDays: 7.0,
			MaxBreakevenDays:   3.0,
		},
	}
}

// d014EconPos XPTUSD 形态持仓+账底全量种子：
//   - 仓 OPEN + 双腿 FILLED（MT 买 1 手 / okx 卖 1 手 fee_usd=2.08 → commission 现金流行 D-013）
//   - broker_accounts 3001157 swap_free=true（swap=0/account_flag 不回退 spec）
//   - facts：okx XPTUSDT funding（fundingAnn 年化%）+ MT spec swap_long（须被 swap_free 旗吞掉）
func d014EconPos(t *testing.T, s *pgstore.Store, fundingAnn float64) *pgstore.Position {
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
	pa, fee := 4000.0, 2.08
	for _, l := range []pgstore.PositionLeg{
		{PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: "3001157:mt5", Symbol: "XPTUSD", Side: "BUY", Qty: 1, Status: "FILLED", PriceActual: &pa},
		{PositionID: posID, LegNo: 2, Phase: "OPEN", Venue: "okx:perp", Symbol: "XPT-USDT-SWAP", Side: "SELL", Qty: 1, Status: "FILLED", FeeUSD: &fee},
	} {
		if err := s.CreatePositionLeg(ctx, &l); err != nil {
			t.Fatalf("create leg: %v", err)
		}
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, commission_bps, swap_free)
		 VALUES ('3001157','mt5','h',443,'p','s','connected',0,true)`); err != nil {
		t.Fatalf("seed broker swap_free: %v", err)
	}
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "XPTUSDT", Value: fundingAnn, Unit: fact.UnitPctAnnualized, Ts: now, Src: "okx"},
		{Kind: fact.KindSwapLong, Venue: "3001157", Symbol: "XPTUSD", Value: -1.28, Unit: fact.UnitBpsDaily, Ts: now, Src: "mt5"},
	}); err != nil {
		t.Fatalf("seed facts: %v", err)
	}
	rows, err := s.ListPositions(ctx, "OPEN", 10, 0)
	if err != nil || len(rows) != 1 {
		t.Fatalf("list pos: rows=%d err=%v", len(rows), err)
	}
	return &rows[0]
}

// TestD014_HarvestEcon econ 收割闸矩阵：NetIfClosedNow≥0 ∧ ≥CarryPerDay×7 才收割。
func TestD014_HarvestEcon(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name     string
		funding  float64 // okx funding 年化 %（SELL 腿 carry=+rate）
		buyBid   float64 // basis=(buyBid−4000)/4000×1e4（sellAsk=4000，anchor 不设→Capture 保守 0）
		quoteAge time.Duration
		wantTrig bool
		wantHold string // 非空=断言 reason 前缀
	}{
		// carry=20.8%/yr → $2.37/天；capture=basis_bps×0.416；costs=2.08+2.60=4.68。
		{"收割: basis=+40bps capture≈$16.6+4.68 net≥carry×7", 20.8, 4016.0, 0, true, ""},
		{"净正不足7日carry: basis=+16bps net≈+2.0<16.6", 20.8, 4006.4, 0, false, ""},
		{"净负永不收: basis=-8bps capture≈1.8 net≈-2.9", 20.8, 3996.8, 0, false, ""},
		{"basis=anchor-区: basis=-14bps net≈-5.3", 20.8, 3994.4, 0, false, ""},
		{"报价超龄→不判", 20.8, 4016.0, 10 * time.Minute, false, "hold_econ"},
		{"carry 微正 5%/yr: basis+40bps net≈11.9≥0.57×7=4.0 收割", 5.0, 4016.0, 0, true, ""},
	}
	for _, tc := range cases {
		s := testAccumStore(t)
		d311Truncate(t, s)
		p := d014EconPos(t, s, tc.funding)
		// anchor=−12.39 冻结（expected_entry_spread_bps）；Capture=(basis−anchor)×N/1e4
		if _, err := s.Pool().Exec(ctx, `UPDATE positions SET expected_entry_spread_bps=-12.39 WHERE id=$1`, p.ID); err != nil {
			t.Fatalf("set anchor: %v", err)
		}
		rows, _ := s.ListPositions(ctx, "OPEN", 10, 0)
		p = &rows[0]
		e := d014EconEngine(s, d404Quotes(time.Now().UTC(), tc.buyBid, 4000, tc.quoteAge), true)
		close, reason := e.harvestCheck(ctx, p)
		if close != tc.wantTrig {
			t.Errorf("%s: close=%v want=%v reason=%q", tc.name, close, tc.wantTrig, reason)
		}
		if tc.wantHold != "" && !containsSub(reason, tc.wantHold) {
			t.Errorf("%s: reason %q 应含 %q", tc.name, reason, tc.wantHold)
		}
		if close && !containsSub(reason, "harvest") {
			t.Errorf("%s: reason 缺 harvest 前缀 %q", tc.name, reason)
		}
	}
}

// TestD014_HarvestFeeClosed 费率源缺 → hold_econ 不收割（fail-closed）。
func TestD014_HarvestFeeClosed(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d014EconPos(t, s, 20.8)
	e := d014EconEngine(s, d404Quotes(time.Now().UTC(), 4016.0, 4000, 0), false)
	if close, reason := e.harvestCheck(ctx, p); close || !containsSub(reason, "hold_econ") {
		t.Fatalf("费率源缺应 hold_econ 不收割, close=%v reason=%q", close, reason)
	}
}

// TestD014_HarvestNoCarryFacts funding fact 缺 → hold_econ 不收割。
func TestD014_HarvestNoCarryFacts(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d014EconPos(t, s, 20.8)
	if _, err := s.Pool().Exec(ctx, `DELETE FROM facts`); err != nil {
		t.Fatalf("clean facts: %v", err)
	}
	e := d014EconEngine(s, d404Quotes(time.Now().UTC(), 4016.0, 4000, 0), true)
	if close, reason := e.harvestCheck(ctx, p); close || !containsSub(reason, "hold_econ") {
		t.Fatalf("carry facts 缺应 hold_econ 不收割, close=%v reason=%q", close, reason)
	}
}

// TestD014_SwapFreeFlag 3001157 swap_free=true → swapRateFor (0,account_flag) 不回退 spec
// （spec fact −1.28bps/天在场也被吞）；无旗账户照常 spec。
func TestD014_SwapFreeFlag(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d014EconPos(t, s, 20.8)
	_ = p
	e := d014EconEngine(s, bus.New(), true)
	bps, src := e.swapRateFor(ctx, "3001157", "XPTUSD", "BUY", 0)
	if bps != 0 || src != swapSrcAccountFlag {
		t.Fatalf("swap_free 账户 want (0,account_flag), got (%.2f,%s)", bps, src)
	}
	// 无旗对照：spec fact −1.28 应生效
	if _, err := s.Pool().Exec(ctx, `UPDATE broker_accounts SET swap_free=false WHERE venue='3001157'`); err != nil {
		t.Fatalf("clear flag: %v", err)
	}
	bps2, src2 := e.swapRateFor(ctx, "3001157", "XPTUSD", "BUY", 0)
	if src2 != swapSrcSpec || bps2 != -1.28 {
		t.Fatalf("无旗账户 want (-1.28,spec), got (%.2f,%s)", bps2, src2)
	}
}

// TestD014_EntryGate 入场闸 #505 回放：carry 20.8%/yr → BreakevenDays≈2.0≤3.0 放行；
// 11.6%/yr → ≈3.6>3.0 拒开；费率源缺 → error fail-closed。
func TestD014_EntryGate(t *testing.T) {
	ctx := context.Background()
	now := time.Now().UTC()
	cases := []struct {
		funding float64
		wantOK  bool
	}{
		{20.8, true}, {11.6, false},
	}
	for _, tc := range cases {
		s := testAccumStore(t)
		d311Truncate(t, s)
		d014EconPos(t, s, tc.funding) // 账底种子（broker/facts）
		opp := pgstore.Opportunity{ID: "o", Type: "XASSET_CARRY", Canonical: "XPTUSD",
			CanonicalSell: d341strptr("XPTUSDT"), VenueBuy: "3001157:mt5", VenueSell: "okx:perp",
			ExpiresAt: now.Add(time.Hour)}
		e := d014EconEngine(s, d404Quotes(now, 4000.0, 4000, 0), true)
		snap, err := e.econEntrySnapshot(ctx, opp, "XPTUSDT", 4160)
		if err != nil {
			t.Fatalf("funding=%.1f: econEntrySnapshot err=%v", tc.funding, err)
		}
		if snap.EntryOK(e.cfg.MaxBreakevenDays) != tc.wantOK {
			t.Errorf("funding=%.1f: EntryOK=%v want=%v (breakeven=%.2f carry=$%.2f)",
				tc.funding, snap.EntryOK(3.0), tc.wantOK, snap.BreakevenDays(), snap.CarryPerDayUSD)
		}
		if tc.funding == 20.8 && (snap.BreakevenDays() > 2.6 || snap.BreakevenDays() < 1.5) {
			t.Errorf("20.8%%: breakeven=%.2f 应≈2.0（设计锚 1.8 量级）", snap.BreakevenDays())
		}
		if tc.funding == 11.6 && snap.BreakevenDays() <= 3.0 {
			t.Errorf("11.6%%: breakeven=%.2f 应>3.0（设计锚 3.2~3.6）", snap.BreakevenDays())
		}
	}
	// 费率源缺 → error fail-closed
	s := testAccumStore(t)
	d311Truncate(t, s)
	d014EconPos(t, s, 20.8)
	e := d014EconEngine(s, d404Quotes(now, 4000.0, 4000, 0), false)
	opp := pgstore.Opportunity{ID: "o", Type: "XASSET_CARRY", Canonical: "XPTUSD",
		CanonicalSell: d341strptr("XPTUSDT"), VenueBuy: "3001157:mt5", VenueSell: "okx:perp",
		ExpiresAt: now.Add(time.Hour)}
	if _, err := e.econEntrySnapshot(ctx, opp, "XPTUSDT", 4160); err == nil {
		t.Fatal("费率源缺应 err（fail-closed 拒开）")
	}
}

// TestD014_EvalNoFeeAmortize eval 行不再出现 hold_fee_amortize/est_days_over——
// econ 装配缺账走 hold_econ:* 观测族（收割链退出 eval 面）。
func TestD014_EvalNoFeeAmortize(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	p := d014EconPos(t, s, 20.8)
	e := d014EconEngine(s, d404Quotes(time.Now().UTC(), 3996.0, 4000, 0), true)
	e.cfg.XAssetExecDriftBps = 100 // 隔离：漂移/灾难带抬阈，只测 harvest 链 eval 面
	e.cfg.XAssetDisasterBps = 1000
	e.fundingBreach = map[int64]int{}
	if close, _ := e.xassetExitCheck(ctx, p); close {
		t.Fatal("净负仓不应收割平仓")
	}
	var dec string
	if err := s.Pool().QueryRow(ctx,
		`SELECT decision FROM position_evals WHERE position_id=$1 ORDER BY evaluated_at DESC LIMIT 1`,
		p.ID).Scan(&dec); err != nil {
		t.Fatalf("eval row: %v", err)
	}
	if dec == "hold_fee_amortize" || containsSub(dec, "est_days_over") {
		t.Fatalf("eval decision=%q 禁含 fee_amortize 族", dec)
	}
}

// d404XAssetPos OPEN XASSET 仓 + 双腿 FILLED + 冻结锚两列 UPDATE 落库。
// anchor/floor 传 nil = NULL（兼容旧测试复用）。ref=PriceActual 4000（basis 换算基准）。
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
	// fin_xasset_monitor 不在 d311Truncate 清单（残留行会干扰 ts<=opened_at 选行）——
	// 本 fixture 独占 pair='XPTUSDT-XPT'，按 pair 清。
	if _, err := s.Pool().Exec(ctx, `DELETE FROM fin_xasset_monitor WHERE pair='XPTUSDT-XPT'`); err != nil {
		t.Fatalf("clean monitor: %v", err)
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
	return &Engine{store: s, quotes: qb, fundingBreach: map[int64]int{}, cfg: gate.Config{XAssetQuoteMaxAge: 5 * time.Minute}}
}

// d014EconEngineCfg D-029 R1 ①：cfg 经装配钩子定制（不直赋字段——config 路径纪律）。
func d014EconEngineCfg(s *pgstore.Store, qb *bus.QuoteBus, feeOK bool, tune func(*gate.Config)) *Engine {
	e := d014EconEngine(s, qb, feeOK)
	if tune != nil {
		tune(&e.cfg)
	}
	return e
}
