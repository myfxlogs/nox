// d471_risk_test.go D-471 S4：风险档案/入场复检/白名单管理面验收锚（先红后绿）。
package dashboard

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/exec"
	"arb/internal/fact"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"

	"connectrpc.com/connect"
)

// d471Fixture cfd 档案底料：MT 腿（3001157:mt5 XAUUSDm cs=100，imr=0.2）
// + CEX perp 腿（binance:perp XAUUSDT ct=0.01），ticks 全额新鲜。
func d471Fixture() *fakeGradDB {
	ct := 100.0
	ctVal := 0.01
	mt := "3001157"
	return &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 7, PaperID: 50, VenueA: "3001157", VenueB: "binance", Symbol: "XAU",
			Structure: "long_cfd_short_perp", MtVenue: &mt, Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 50, VenueA: "3001157", VenueB: "binance", Symbol: "XAU",
			Structure: "long_cfd_short_perp", MtVenue: &mt, QtyUSD: 1000,
			Status: "active", AccruedUSD: 5, EntryTs: time.Now().Add(-6 * 24 * time.Hour),
			EstRoundTripFeeUSD: &[]float64{3.4}[0], EntryBasisBps: &[]float64{10}[0], LastBasisBps: &[]float64{12}[0]},
		maps:  []pgstore.SymbolMapping{{Venue: "3001157", BrokerSymbol: "XAUUSDm", Canonical: "XAUUSD", IsPrimary: true, ContractSize: &ct}},
		accts: []pgstore.BrokerAccount{{Venue: "3001157", Platform: 1}},
		ticks: map[string]pgstore.TickRow{
			"3001157|mt5|XAUUSDm":  {Ts: time.Now(), Bid: 2400, Ask: 2401},
			"binance|perp|XAUUSDT": {Ts: time.Now(), Bid: 2400, Ask: 2401},
		},
		universe: map[string]pgstore.CexUniverseRow{
			"binance|XAUUSDT": {Venue: "binance", Symbol: "XAUUSDT", HedgeCanonical: "XAU", CtVal: &ctVal},
		},
		facts: map[string][]fact.Fact{
			"3001157|": {{Kind: fact.KindInitMarginRate, Value: 0.2, Ts: time.Now()}},
		},
		p95: 25, p95OK: true,
	}
}

// TestD471_Dossier_CfdLegs S4①：双腿量/资金/liq/最坏场景字段断言。
func TestD471_Dossier_CfdLegs(t *testing.T) {
	f := d471Fixture()
	s := &Service{gradStore: f}
	// D-565 S2：venue 杠杆实读缝（夹具钉 5x=原硬编口径）。
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 5, nil }
	s.SetCarryRegime(exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12})
	rep := s.gradDossier(context.Background(), f.grad, f.paper)
	if rep == nil || rep.NotionalUsd == nil || *rep.NotionalUsd != 1000 {
		t.Fatalf("notional: %+v", rep)
	}
	if rep.NotionalSource != "paper" {
		t.Errorf("notional_source=%s want paper", rep.NotionalSource)
	}
	if len(rep.Legs) != 2 {
		t.Fatalf("legs=%d", len(rep.Legs))
	}
	mtLeg, cexLeg := rep.Legs[0], rep.Legs[1]
	// MT 腿：qty=1000/(2400.5×100)≈0.00417 手；margin=1000×0.2=200
	if mtLeg.VenueKey != "3001157:mt5" || mtLeg.Symbol != "XAUUSDm" || mtLeg.Role != "buy" {
		t.Errorf("mt leg %+v", mtLeg)
	}
	if mtLeg.QtyNative == nil || *mtLeg.QtyNative < 0.004 || *mtLeg.QtyNative > 0.005 {
		t.Errorf("mt qty=%v", mtLeg.QtyNative)
	}
	if mtLeg.MarginUsd == nil || *mtLeg.MarginUsd != 200 {
		t.Errorf("mt margin=%v want 200", mtLeg.MarginUsd)
	}
	// CEX 腿：qty=1000/(2400.5×0.01)≈41.66 张；margin=1000/5=200
	if cexLeg.VenueKey != "binance:perp" || cexLeg.Symbol != "XAUUSDT" || cexLeg.Role != "sell" {
		t.Errorf("cex leg %+v", cexLeg)
	}
	if cexLeg.QtyNative == nil || *cexLeg.QtyNative < 40 || *cexLeg.QtyNative > 43 {
		t.Errorf("cex qty=%v", cexLeg.QtyNative)
	}
	if rep.CapitalUsd == nil || *rep.CapitalUsd != 400 {
		t.Errorf("capital=%v want 400", rep.CapitalUsd)
	}
	if rep.LiqDistancePctEst == nil || *rep.LiqDistancePctEst != 19 {
		t.Errorf("liq=%v want (1/5-0.01)*100=19", rep.LiqDistancePctEst)
	}
	if rep.AdverseBasisP95Usd == nil || *rep.AdverseBasisP95Usd != 2.5 {
		t.Errorf("p95=%v want 25bps×1000/1e4=2.5", rep.AdverseBasisP95Usd)
	}
	if rep.RoundtripFeeUsd == nil || *rep.RoundtripFeeUsd != 3.4 {
		t.Errorf("fee=%v want paper 真费 3.4", rep.RoundtripFeeUsd)
	}
}

// TestD471_Dossier_Gaps S4②对抗：解析失败/ticks 超龄→字段 NULL+gaps 非空（禁静默 0）。
func TestD471_Dossier_Gaps(t *testing.T) {
	f := d471Fixture()
	f.ticks["binance|perp|XAUUSDT"] = pgstore.TickRow{Ts: time.Now().Add(-10 * time.Minute), Bid: 2400, Ask: 2401}
	delete(f.facts, "3001157|") // imr 缺 → D-006 S1b 悲观档 20x 估
	s := &Service{gradStore: f}
	// D-565 S2：venue 杠杆实读缝（夹具钉 5x=原硬编口径）。
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 5, nil }
	s.SetCarryRegime(exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12})
	rep := s.gradDossier(context.Background(), f.grad, f.paper)
	if rep.Legs[1].QtyUsd != nil || rep.Legs[1].QtyNative != nil {
		t.Error("tick 超龄腿字段应 NULL（禁静默 0）")
	}
	// D-006 S1b：imr 缺→MT 腿按悲观档 20x 估（1000×0.05=50；cex 腿 tick 超龄无 margin），
	// gaps 注记未实测（不 NULL 不谎报实测）。
	if rep.CapitalUsd == nil || *rep.CapitalUsd != 50 {
		t.Errorf("imr 缺→capital 应按悲观档估 50, got %v", rep.CapitalUsd)
	}
	joined := strings.Join(rep.Gaps, ";")
	if !strings.Contains(joined, "价格数据过期") || !strings.Contains(joined, "init_margin_rate 未实测") {
		t.Errorf("gaps=%s", joined)
	}
}

// TestD471_Approve_NonCfd_DecayedReject A3：非 cfd 衰减行拒批「证据已衰减」（B-013 全结构硬前置）。
func TestD471_Approve_NonCfd_DecayedReject(t *testing.T) {
	fee, entryB, lastB := 0.5, 10.0, 12.0
	f := &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 8, PaperID: 60, VenueA: "binance", Symbol: "SKLUSDT", Structure: "spot_carry", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 60, VenueA: "binance", Structure: "spot_carry", Side: "short_perp",
			Status: "active", AccruedUSD: 5, EntryTs: time.Now().Add(-6 * 24 * time.Hour),
			EstRoundTripFeeUSD: &fee, EntryBasisBps: &entryB, LastBasisBps: &lastB, LowStreak: 1},
	}
	s := &Service{gradStore: f, xassetReloader: &fakeReloader{}}
	// D-565 S2：venue 杠杆实读缝（夹具钉 5x=原硬编口径）。
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 5, nil }
	resp, _ := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{GraduationId: 8, MaxLongQty: 1, MaxShortQty: 1, MaxGrossUsd: 100}))
	if resp.Msg.Ok || !strings.Contains(resp.Msg.Error, "证据已衰减") {
		t.Fatalf("非 cfd 衰减行应拒批，got ok=%v err=%s", resp.Msg.Ok, resp.Msg.Error)
	}
	if f.combo != nil {
		t.Error("非 cfd 拒批/放行均禁写白名单")
	}
}

// TestD471_EntryWindow_SpotCarry spot_carry 复检=perp 腿 FundingRegimeOK 按 Side 定号。
func TestD471_EntryWindow_SpotCarry(t *testing.T) {
	fee, entryB, lastB := 0.5, 10.0, 12.0
	paper := &pgstore.PaperSpotCarry{ID: 61, VenueA: "binance", Symbol: "SKLUSDT", Structure: "spot_carry", Side: "short_perp",
		Status: "active", EstRoundTripFeeUSD: &fee, EntryBasisBps: &entryB, LastBasisBps: &lastB}
	grad := &pgstore.CarryGraduation{ID: 9, PaperID: 61, VenueA: "binance", Symbol: "SKLUSDT", Structure: "spot_carry", Status: "pending"}
	f := &fakeGradDB{}
	s := &Service{gradStore: f}
	// D-565 S2：venue 杠杆实读缝（夹具钉 5x=原硬编口径）。
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 5, nil }
	s.SetCarryRegime(exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12})
	// short_perp=收正 funding（sign=+1）：窗口全正→open
	f.fundingWin = map[string][]pgstore.FactPoint{"binance|SKLUSDT": {{Value: 10}, {Value: 11}, {Value: 12}, {Value: 10}, {Value: 11}, {Value: 12}, {Value: 10}, {Value: 11}, {Value: 12}, {Value: 10}, {Value: 11}, {Value: 12}}}
	if w := s.entryWindowOf(context.Background(), grad, paper); w != "open" {
		t.Errorf("funding 正窗口应 open，got %s", w)
	}
	// 翻负→closed
	f.fundingWin["binance|SKLUSDT"][0].Value = -5
	if w := s.entryWindowOf(context.Background(), grad, paper); !strings.HasPrefix(w, "closed:") {
		t.Errorf("funding 翻号应 closed，got %s", w)
	}
	// long_perp（sign=−1）：窗口负值=收入腿→open
	paper.Side = "long_perp"
	f.fundingWin = map[string][]pgstore.FactPoint{"binance|SKLUSDT": {{Value: -10}, {Value: -11}, {Value: -12}, {Value: -10}, {Value: -11}, {Value: -12}, {Value: -10}, {Value: -11}, {Value: -12}, {Value: -10}, {Value: -11}, {Value: -12}}}
	if w := s.entryWindowOf(context.Background(), grad, paper); w != "open" {
		t.Errorf("long_perp 负 funding 窗口应 open，got %s", w)
	}
}

// TestD471_UpsertCombo S3：insert+toggle+reloader nil 拒写+在持仓 confirm+ caps 下调锁+preview 校验。
func TestD471_UpsertCombo(t *testing.T) {
	ctx := context.Background()
	newSvc := func(f *fakeGradDB) *Service {
		s := &Service{gradStore: f, xassetReloader: &fakeReloader{}, xassetEnv: "real",
			pipeline: nil}
		s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 5, nil }
		return s
	}
	// reloader nil → 拒写
	f := &fakeGradDB{}
	s := newSvc(f)
	s.xassetReloader = nil
	resp, _ := s.UpsertXAssetAllowCombo(ctx, connect.NewRequest(&v1.UpsertXAssetAllowComboRequest{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT", VenueBuy: "3001157:mt5", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUsd: 5000}))
	if resp.Msg.Ok {
		t.Error("reloader nil 应 fail-closed 拒写")
	}
	// 非法后缀
	s = newSvc(&fakeGradDB{})
	resp, _ = s.UpsertXAssetAllowCombo(ctx, connect.NewRequest(&v1.UpsertXAssetAllowComboRequest{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT", VenueBuy: "3001157", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUsd: 5000}))
	if resp.Msg.Ok || !strings.Contains(resp.Msg.Error, "后缀非法") {
		t.Errorf("非法 venue 后缀应拒，got %s", resp.Msg.Error)
	}
	// D-500 DS2：id=0 管理面插入=路B已关闭——拒写提示走毕业授权（fake Insert 零调用）。
	f = &fakeGradDB{}
	s = newSvc(f)
	resp, _ = s.UpsertXAssetAllowCombo(ctx, connect.NewRequest(&v1.UpsertXAssetAllowComboRequest{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT", VenueBuy: "3001157:mt5", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUsd: 5000, Enabled: true}))
	if resp.Msg.Ok || !strings.Contains(resp.Msg.Error, "毕业授权") {
		t.Fatalf("路B insert 应拒，got ok=%v err=%s", resp.Msg.Ok, resp.Msg.Error)
	}
	// update：无在持仓→直接执行
	f = &fakeGradDB{adminRows: []pgstore.XAssetComboAdminRow{{ID: 3}}}
	s = newSvc(f)
	resp, _ = s.UpsertXAssetAllowCombo(ctx, connect.NewRequest(&v1.UpsertXAssetAllowComboRequest{
		Id: 3, CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT", VenueBuy: "3001157:mt5", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUsd: 5000, Enabled: false}))
	if !resp.Msg.Ok || f.updated == nil || f.updated.Enabled {
		t.Fatalf("update 应执行 got ok=%v updated=%+v", resp.Msg.Ok, f.updated)
	}
	// 在持仓 toggle→needs_confirm
	f = &fakeGradDB{comboOpen: map[string][]int64{"XAUUSD|3001157:mt5": {502}}, comboOpenN: map[string]float64{"XAUUSD|3001157:mt5": 200}}
	s = newSvc(f)
	resp, _ = s.UpsertXAssetAllowCombo(ctx, connect.NewRequest(&v1.UpsertXAssetAllowComboRequest{
		Id: 3, CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT", VenueBuy: "3001157:mt5", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUsd: 5000, Enabled: false}))
	if !resp.Msg.NeedsConfirm || len(resp.Msg.OpenPositionIds) != 1 || resp.Msg.OpenPositionIds[0] != 502 {
		t.Fatalf("在持仓 toggle 应 needs_confirm，got %+v", resp.Msg)
	}
	if f.updated != nil {
		t.Error("未 confirm 不得执行")
	}
	// confirm=true→执行
	resp, _ = s.UpsertXAssetAllowCombo(ctx, connect.NewRequest(&v1.UpsertXAssetAllowComboRequest{
		Id: 3, CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT", VenueBuy: "3001157:mt5", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUsd: 5000, Enabled: false, Confirm: true}))
	if !resp.Msg.Ok {
		t.Fatalf("confirm 应执行，got %s", resp.Msg.Error)
	}
	// B-020：新 caps < 在持名义 200→拒改
	f = &fakeGradDB{comboOpen: map[string][]int64{"XAUUSD|3001157:mt5": {502}}, comboOpenN: map[string]float64{"XAUUSD|3001157:mt5": 200}}
	s = newSvc(f)
	resp, _ = s.UpsertXAssetAllowCombo(ctx, connect.NewRequest(&v1.UpsertXAssetAllowComboRequest{
		Id: 3, CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT", VenueBuy: "3001157:mt5", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUsd: 100, Enabled: true, Confirm: true}))
	if resp.Msg.Ok || !strings.Contains(resp.Msg.Error, "在持 pos") {
		t.Fatalf("caps 低于在持名义应拒改，got ok=%v err=%s", resp.Msg.Ok, resp.Msg.Error)
	}
	// Preview 三拒
	s = newSvc(&fakeGradDB{})
	for _, bad := range []*v1.PreviewXAssetComboRiskRequest{
		{CanonicalBuy: "X", VenueBuy: "a:perp", VenueSell: "b:perp", NotionalUsd: 0},
		{CanonicalBuy: "X", VenueSell: "b:perp", NotionalUsd: 100},
		{CanonicalBuy: "X", VenueBuy: "a:bad", VenueSell: "b:perp", NotionalUsd: 100},
	} {
		r, _ := s.PreviewXAssetComboRisk(ctx, connect.NewRequest(bad))
		if r.Msg.Ok {
			t.Errorf("preview 非法入参 %+v 应拒", bad)
		}
	}
	// Preview 正常
	f = d471Fixture()
	s = newSvc(f)
	s.SetCarryRegime(exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12})
	r, _ := s.PreviewXAssetComboRisk(ctx, connect.NewRequest(&v1.PreviewXAssetComboRiskRequest{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT", VenueBuy: "3001157:mt5", VenueSell: "binance:perp", NotionalUsd: 500}))
	if !r.Msg.Ok || r.Msg.Risk == nil || r.Msg.Risk.NotionalUsd == nil || *r.Msg.Risk.NotionalUsd != 500 {
		t.Fatalf("preview %+v", r.Msg)
	}
}
