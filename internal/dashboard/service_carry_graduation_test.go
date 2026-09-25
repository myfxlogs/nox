// service_carry_graduation_test.go —— D-403 S6：approve 全分支（fake store+fake reloader+fake automap）。
// 覆盖：combo 插入字段正确（双 structure 方向）/pending 守卫/caps 必填/venue gap 命中
// （1279967:mt5 型）/非 cfd 体检分支不写白名单/reject 流转/唯一键撞/reloader 未接线 fail-closed。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"arb/internal/exec"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
	"connectrpc.com/connect"
)

type fakeGradDB struct {
	grad  *pgstore.CarryGraduation
	paper *pgstore.PaperSpotCarry
	maps  []pgstore.SymbolMapping
	accts []pgstore.BrokerAccount

	combo        *pgstore.XAssetComboInsert
	decision     string
	decisionNote string
	comboErr     error
	reloaded     int
	alerts       int
	flags        []pgstore.VenueSymbolFlag
	list         []pgstore.CarryGraduation
	// D-475a S3/S4：flag/caps/吊销/在管仓测试面。
	activeFlags   map[string]pgstore.VenueSymbolFlag
	caps          map[string]pgstore.VenueExposureCap
	disabledCalls []string
	disableRet    int64
	openPos       map[string][]int64
	allowRows     []pgstore.XAssetAllowRow
	facts         map[string][]fact.Fact // venue|symbol → facts（S5 interval 活读）
	// D-471 S1/S2/S3：dossier/复检/管理面测试面。
	ticks      map[string]pgstore.TickRow // venue|venue_type|symbol → tick
	universe   map[string]pgstore.CexUniverseRow
	fundingWin map[string][]pgstore.FactPoint // venue|symbol → funding 预测流
	p95        float64
	p95OK      bool
	snaps      []pgstore.VenueAccountLatest
	adminRows  []pgstore.XAssetComboAdminRow
	comboOpen  map[string][]int64 // canonicalBuy|venueBuy → 在持仓 posIDs
	comboOpenN map[string]float64
	updated    *pgstore.XAssetComboAdminRow
	updateErr  error
}

func (f *fakeGradDB) GetCarryGraduation(context.Context, int64) (*pgstore.CarryGraduation, error) {
	return f.grad, nil
}
func (f *fakeGradDB) GetPaperSpotCarryByID(context.Context, int64) (*pgstore.PaperSpotCarry, error) {
	return f.paper, nil
}
func (f *fakeGradDB) ListSymbolMappings(context.Context, string) ([]pgstore.SymbolMapping, error) {
	return f.maps, nil
}
func (f *fakeGradDB) ListBrokerAccounts(context.Context) ([]pgstore.BrokerAccount, error) {
	return f.accts, nil
}
func (f *fakeGradDB) InsertXAssetAllowCombo(_ context.Context, r *pgstore.XAssetComboInsert) (int64, error) {
	if f.comboErr != nil {
		return 0, f.comboErr
	}
	c := *r
	f.combo = &c
	return 77, nil
}
func (f *fakeGradDB) UpdateCarryGraduationDecision(_ context.Context, _ int64, status string, _ *int64, note string) error {
	f.decision = status
	f.decisionNote = note
	return nil
}
func (f *fakeGradDB) InsertAlert(context.Context, *int64, string, string) error {
	f.alerts++
	return nil
}
func (f *fakeGradDB) ListCarryGraduations(context.Context) ([]pgstore.CarryGraduation, error) {
	return f.list, nil
}

// D-475a S3/S4 扩面。
func (f *fakeGradDB) ListActiveFlags(context.Context) (map[string]pgstore.VenueSymbolFlag, error) {
	if f.activeFlags == nil {
		return map[string]pgstore.VenueSymbolFlag{}, nil
	}
	return f.activeFlags, nil
}
func (f *fakeGradDB) ListVenueExposureCaps(context.Context) (map[string]pgstore.VenueExposureCap, error) {
	if f.caps == nil {
		return map[string]pgstore.VenueExposureCap{}, nil
	}
	return f.caps, nil
}
func (f *fakeGradDB) DisableCombosByFlag(_ context.Context, bareVenue, symbol string) (int64, error) {
	f.disabledCalls = append(f.disabledCalls, bareVenue+"|"+symbol)
	return f.disableRet, nil
}
func (f *fakeGradDB) CountOpenPositionsOnVenue(_ context.Context, bareVenue string) (int64, []int64, error) {
	if f.openPos != nil {
		if r, ok := f.openPos[bareVenue]; ok {
			return int64(len(r)), r, nil
		}
	}
	return 0, nil, nil
}
func (f *fakeGradDB) LatestFacts(_ context.Context, kind, venue, symbol string) ([]fact.Fact, error) {
	if f.facts == nil {
		return nil, nil
	}
	return f.facts[venue+"|"+symbol], nil
}

// —— D-471 fake 面 ——
func (f *fakeGradDB) LatestTick(_ context.Context, venue, venueType, symbol string) (pgstore.TickRow, bool, error) {
	if f.ticks == nil {
		return pgstore.TickRow{}, false, nil
	}
	t, ok := f.ticks[venue+"|"+venueType+"|"+symbol]
	return t, ok, nil
}
func (f *fakeGradDB) ListCexUniverse(context.Context) (map[string]pgstore.CexUniverseRow, error) {
	if f.universe == nil {
		return map[string]pgstore.CexUniverseRow{}, nil
	}
	return f.universe, nil
}
func (f *fakeGradDB) FundingPredWindow(_ context.Context, venue, symbol string, _ time.Time) ([]pgstore.FactPoint, error) {
	if f.fundingWin == nil {
		return nil, nil
	}
	return f.fundingWin[venue+"|"+symbol], nil
}
func (f *fakeGradDB) XAssetMonitorSpreadP95(context.Context, string, string) (float64, bool, error) {
	return f.p95, f.p95OK, nil
}
func (f *fakeGradDB) LatestVenueAccountSnapshots(context.Context) ([]pgstore.VenueAccountLatest, error) {
	return f.snaps, nil
}
func (f *fakeGradDB) ListXAssetAllowCombosAll(context.Context) ([]pgstore.XAssetComboAdminRow, error) {
	return f.adminRows, nil
}
func (f *fakeGradDB) UpdateXAssetAllowCombo(_ context.Context, r *pgstore.XAssetComboAdminRow) (bool, error) {
	if f.updateErr != nil {
		return false, f.updateErr
	}
	cp := *r
	f.updated = &cp
	return true, nil
}
func (f *fakeGradDB) ListOpenPositionsForCombo(_ context.Context, cb, cs, vb, vs string) ([]int64, float64, error) {
	if f.comboOpen == nil {
		return nil, 0, nil
	}
	ids := f.comboOpen[cb+"|"+vb]
	return ids, f.comboOpenN[cb+"|"+vb], nil
}

func (f *fakeGradDB) LoadXAssetAllowCombos(_ context.Context, environment string) ([]pgstore.XAssetAllowRow, error) {
	if f.allowRows != nil {
		return f.allowRows, nil
	}
	return []pgstore.XAssetAllowRow{{
		CanonicalBuy: "BTCUSDT", CanonicalSell: "BTCUSD", VenueBuy: "gate:perp", VenueSell: "1279967:mt5",
		MaxLongQty: 1000, MaxShortQty: 0.01, MaxGrossUSD: 5000,
	}}, nil
}

type fakeReloader struct{ calls int }

func (f *fakeReloader) ReloadXAssetAllowCombos(context.Context) error { f.calls++; return nil }

func gradFixture(structure string) (*fakeGradDB, *Service) {
	mt := "1279967"
	// D-474b S2：approve 复核闸按同一判据现算——fixture paper 行必须是合格证据行
	// （active≥5d+accrued>0+est_fee/basis 齐），否则旧用例全被复核闸拦。
	fee, entryB, lastB := 0.5, 10.0, 12.0
	f := &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 5, PaperID: 42, Venue: "1279967×gate", Symbol: "BTC", Structure: structure, MtVenue: &mt, Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 42, Venue: "1279967×gate", Symbol: "BTC", Structure: structure,
			Status: "active", AccruedUSD: 5.0, EntryTs: time.Now().Add(-6 * 24 * time.Hour),
			EstRoundTripFeeUSD: &fee, EntryBasisBps: &entryB, LastBasisBps: &lastB, Side: "long"},
		maps:  []pgstore.SymbolMapping{{Venue: "1279967", BrokerSymbol: "BTCUSD", Canonical: "BTCUSD", IsPrimary: true}},
		accts: []pgstore.BrokerAccount{{Venue: "1279967", Platform: 1}},
	}
	s := &Service{
		gradStore: f, xassetReloader: &fakeReloader{},
		allowVenuesSource: func(context.Context) (map[string]bool, error) {
			return map[string]bool{"gate:perp": true}, nil
		},
		xassetEnv: "real",
		pipeline:  exec.NewPipeline(exec.NewRegistry(), nil, nil, exec.RiskLimits{}, "real"),
	}
	s.autoMapFn = func(context.Context, string) (string, error) { return "applied=0 skipped=3 conflicts=0", nil }
	return f, s
}

// TestD403_Approve_FanOutRefresh R2：approve 后 pipeline+自快照两链刷新（Match 立即可见）。
func TestD403_Approve_FanOutRefresh(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	resp, err := s.ApproveCarryGraduation(context.Background(), approveReq())
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v errstr=%s", resp.Msg.Ok, err, resp.Msg.Error)
	}
	// pipeline 链：fake store rows（模拟 DB 已写入）经扇出后立即可见
	if spec := exec.MatchXAssetComboAllow(s.pipeline.XAssetAllowSpecs(), "BTCUSDT", "BTCUSD", "gate:perp", "1279967:mt5"); spec == nil {
		t.Error("扇出后 pipeline 快照应命中新 combo（免重启）")
	}
	// 自链（手动执行读点）
	if spec := exec.MatchXAssetComboAllow(s.xassetAllowSpecs(), "BTCUSDT", "BTCUSD", "gate:perp", "1279967:mt5"); spec == nil {
		t.Error("扇出后自快照应命中新 combo")
	}
	_ = f
}

func approveReq() *connect.Request[v1.ApproveCarryGraduationRequest] {
	return connect.NewRequest(&v1.ApproveCarryGraduationRequest{
		GraduationId: 5, MaxLongQty: 0.01, MaxShortQty: 1000, MaxGrossUsd: 5000,
	})
}

func TestD403_Approve_LongCfd_ShortPerp_Direction(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp") // buy=MT sell=CEX
	resp, err := s.ApproveCarryGraduation(context.Background(), approveReq())
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v errstr=%s", resp.Msg.Ok, err, resp.Msg.Error)
	}
	if f.combo == nil {
		t.Fatal("combo 未写入")
	}
	if f.combo.CanonicalBuy != "BTCUSD" || f.combo.CanonicalSell != "BTCUSDT" {
		t.Errorf("long_cfd: buy=MT 卖=CEX，got %s|%s", f.combo.CanonicalBuy, f.combo.CanonicalSell)
	}
	if f.combo.VenueBuy != "1279967:mt5" || f.combo.VenueSell != "gate:perp" {
		t.Errorf("venues got %s×%s", f.combo.VenueBuy, f.combo.VenueSell)
	}
	if f.combo.Simulated {
		t.Error("simulated 必须 false（真仓白名单）")
	}
	if f.combo.MaxLongQty != 0.01 || f.combo.MaxShortQty != 1000 || f.combo.MaxGrossUSD != 5000 {
		t.Errorf("caps: %+v", f.combo)
	}
	if f.decision != "approved" {
		t.Errorf("decision=%s", f.decision)
	}
	// venue gap：1279967:mt5 不在 env 快照 → 命中报告（D-475a 后另有敞口未设闸 warn）
	if len(resp.Msg.VenueGaps) == 0 || resp.Msg.VenueGaps[0] != "1279967:mt5" {
		t.Errorf("venue_gaps got %v, want 首条=[1279967:mt5]", resp.Msg.VenueGaps)
	}
}

func TestD403_Approve_ShortCfd_LongPerp_DirectionReversed(t *testing.T) {
	f, s := gradFixture("short_cfd_long_perp") // buy=CEX sell=MT（三轮自审计方向反转修）
	resp, err := s.ApproveCarryGraduation(context.Background(), approveReq())
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v errstr=%s", resp.Msg.Ok, err, resp.Msg.Error)
	}
	if f.combo.CanonicalBuy != "BTCUSDT" || f.combo.CanonicalSell != "BTCUSD" {
		t.Errorf("short_cfd: buy=CEX 卖=MT，got %s|%s", f.combo.CanonicalBuy, f.combo.CanonicalSell)
	}
	if f.combo.VenueBuy != "gate:perp" || f.combo.VenueSell != "1279967:mt5" {
		t.Errorf("venues got %s×%s", f.combo.VenueBuy, f.combo.VenueSell)
	}
}

func TestD403_Approve_Guards(t *testing.T) {
	// caps 缺失
	f, s := gradFixture("long_cfd_short_perp")
	resp, _ := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{GraduationId: 5, MaxLongQty: 1, MaxShortQty: 1}))
	if resp.Msg.Ok || f.combo != nil {
		t.Error("max_gross_usd 缺失应拒绝且不写库")
	}
	// 非 pending
	f, s = gradFixture("long_cfd_short_perp")
	f.grad.Status = "approved"
	resp, _ = s.ApproveCarryGraduation(context.Background(), approveReq())
	if resp.Msg.Ok {
		t.Error("非 pending 应拒绝")
	}
	// 唯一键撞
	f, s = gradFixture("long_cfd_short_perp")
	f.comboErr = pgstore.ErrXAssetComboExists
	resp, _ = s.ApproveCarryGraduation(context.Background(), approveReq())
	if resp.Msg.Ok || !contains2(resp.Msg.Error, "已在白名单") {
		t.Errorf("唯一键撞应报已在白名单，got %s", resp.Msg.Error)
	}
	// reloader 未接线 fail-closed
	f, s = gradFixture("long_cfd_short_perp")
	s.xassetReloader = nil
	resp, _ = s.ApproveCarryGraduation(context.Background(), approveReq())
	if resp.Msg.Ok || f.combo != nil {
		t.Error("重载器未接线应 fail-closed 且不写库")
	}
	// notional 显式非正
	f, s = gradFixture("long_cfd_short_perp")
	neg := -1.0
	resp, _ = s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{GraduationId: 5, MaxLongQty: 1, MaxShortQty: 1, MaxGrossUsd: 100, NotionalUsd: &neg}))
	if resp.Msg.Ok {
		t.Error("notional 非正应拒绝")
	}
}

func TestD403_Approve_NonCfd_LinkReport_NoCombo(t *testing.T) {
	// D-471 S2：非 cfd 亦走全结构硬前置——fixture 须合格（active≥5d+accrued>0+est_fee/basis 齐）。
	fee, entryB, lastB := 0.5, 10.0, 12.0
	f := &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 6, PaperID: 43, Venue: "gate×okx", Symbol: "BTC", Structure: "perp_perp", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 43, Venue: "gate×okx", Structure: "perp_perp",
			Status: "active", AccruedUSD: 5.0, EntryTs: time.Now().Add(-6 * 24 * time.Hour),
			EstRoundTripFeeUSD: &fee, EntryBasisBps: &entryB, LastBasisBps: &lastB},
	}
	s := &Service{gradStore: f, xassetReloader: &fakeReloader{}, allowVenuesSource: func(context.Context) (map[string]bool, error) {
		return map[string]bool{"gate:perp": true}, nil
	}}
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{GraduationId: 6, MaxLongQty: 1, MaxShortQty: 1, MaxGrossUsd: 100}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v", resp.Msg.Ok, err)
	}
	if f.combo != nil {
		t.Error("非 cfd 结构禁写白名单（SSOT §1 边界）")
	}
	if resp.Msg.LinkReport == "" {
		t.Error("非 cfd approve 应返回链路体检报告")
	}
	if len(resp.Msg.VenueGaps) != 1 || resp.Msg.VenueGaps[0] != "okx" {
		t.Errorf("venue_gaps got %v, want [okx]", resp.Msg.VenueGaps)
	}
}

func TestD403_Reject_Flow(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	resp, err := s.RejectCarryGraduation(context.Background(), connect.NewRequest(&v1.RejectCarryGraduationRequest{GraduationId: 5, Note: "证据不足"}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v", resp.Msg.Ok, err)
	}
	if f.decision != "rejected" || f.decisionNote != "证据不足" {
		t.Errorf("decision=%s note=%q", f.decision, f.decisionNote)
	}
	if f.combo != nil {
		t.Error("reject 禁写白名单")
	}
}

func contains2(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || index2(s, sub) >= 0)
}

func index2(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

// TestD403R2_FanOut_Race R2 race 锚：手动执行链读侧（xassetAllowSpecs+Match）vs
// approve 扇出写（refreshXAssetSnapshots 两处 Store），-race 下零 DATA RACE 即过（×3 外层）。
func TestD403R2_FanOut_Race(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	_ = f
	done := make(chan struct{})
	go func() { // 读侧：模拟 Execute 内部组合闸
		defer close(done)
		for i := 0; i < 500; i++ {
			_ = exec.MatchXAssetComboAllow(s.xassetAllowSpecs(), "BTCUSDT", "BTCUSD", "gate:perp", "1279967:mt5")
		}
	}()
	for i := 0; i < 50; i++ {
		if err := s.refreshXAssetSnapshots(context.Background()); err != nil {
			t.Fatalf("refresh: %v", err)
		}
	}
	<-done
}

// TestD443_AllowVenuesSourceLive T5：venue gap 报告走活读 getter（源变化→gap 跟随），
// error→空集保守=全缺口。
func TestD443_AllowVenuesSourceLive(t *testing.T) {
	allow := map[string]bool{"gate:perp": true}
	s := &Service{allowVenuesSource: func(context.Context) (map[string]bool, error) { return allow, nil }}
	g := s.allowVenuesNow(context.Background())
	if !g["gate:perp"] {
		t.Fatalf("活读应返回源集合, got %v", g)
	}
	allow = map[string]bool{"binance:perp": true} // 源变化（业主改行）
	if g := s.allowVenuesNow(context.Background()); !g["binance:perp"] || g["gate:perp"] {
		t.Errorf("gap 报告应跟随表实时态, got %v", g)
	}
	s2 := &Service{allowVenuesSource: func(context.Context) (map[string]bool, error) {
		return nil, fmt.Errorf("db down")
	}}
	if g := s2.allowVenuesNow(context.Background()); len(g) != 0 {
		t.Errorf("error→空集保守=全缺口, got %v", g)
	}
	var s3 Service // nil 源 → 空集保守
	if g := s3.allowVenuesNow(context.Background()); len(g) != 0 {
		t.Errorf("nil 源→空集, got %v", g)
	}
}

func (f *fakeGradDB) InsertFlag(_ context.Context, fl *pgstore.VenueSymbolFlag) error {
	f.flags = append(f.flags, *fl)
	return nil
}

// TestD463_RejectWriteFlag T3：reject write_flag=true → flags 表投影行（level/note 复用）；
// 未勾选不写；flag_warn 透出 ListCarryGraduations 行。
func TestD463_RejectWriteFlag(t *testing.T) {
	f, s := gradFixture("spot_carry")
	resp, err := s.RejectCarryGraduation(context.Background(), connect.NewRequest(&v1.RejectCarryGraduationRequest{
		GraduationId: 5, Note: "XPD funding 字段语义坑", WriteFlag: true, FlagLevel: "blocked",
	}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v", resp.Msg.Ok, err)
	}
	if len(f.flags) != 1 || f.flags[0].Level != "blocked" || f.flags[0].Reason != "XPD funding 字段语义坑" ||
		f.flags[0].Venue != "1279967×gate" || f.flags[0].Symbol != "BTC" {
		t.Fatalf("flags=%+v", f.flags)
	}
	// 未勾选 → 不写。
	f2, s2 := gradFixture("spot_carry")
	f2.grad.Status = "pending"
	if _, err := s2.RejectCarryGraduation(context.Background(), connect.NewRequest(&v1.RejectCarryGraduationRequest{
		GraduationId: 5, Note: "仅拒绝"})); err != nil {
		t.Fatal(err)
	}
	if len(f2.flags) != 0 {
		t.Fatal("write_flag=false 禁写旗标")
	}
	// flag_warn 透出 list 行。
	warn := "⚠funding 语义待查"
	f3, s3 := gradFixture("spot_carry")
	f3.list = []pgstore.CarryGraduation{{ID: 7, PaperID: 1, Venue: "gate", Symbol: "XPD", Status: "pending", FlagWarn: &warn}}
	lr, err := s3.ListCarryGraduations(context.Background(), connect.NewRequest(&v1.ListCarryGraduationsRequest{}))
	if err != nil || len(lr.Msg.Rows) != 1 || lr.Msg.Rows[0].FlagWarn == nil || *lr.Msg.Rows[0].FlagWarn != warn {
		t.Fatalf("rows=%+v err=%v", lr.Msg.Rows, err)
	}
}

// TestD474b_ApproveDegradedReject A3：paper 行中途衰减（low_streak>0）→复核闸拒批，
// 白名单零写入、建议行仍 pending（不落 approved）。
func TestD474b_ApproveDegradedReject(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	f.paper.LowStreak = 2 // 衰减：低息在轨 → GraduationQualifiedReason 拒
	resp, err := s.ApproveCarryGraduation(context.Background(), approveReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Ok {
		t.Fatal("衰减行不得批准")
	}
	if !strings.Contains(resp.Msg.Error, "证据已衰减") { // D-471：闸上移至分叉前，文案随改
		t.Fatalf("error=%s", resp.Msg.Error)
	}
	if f.combo != nil {
		t.Fatal("拒批禁写白名单")
	}
	if f.decision == "approved" {
		t.Fatal("建议行不得落 approved")
	}
}

// TestD474b_ApproveQualifiedPass 正向锚：合格行复核闸放行（防闸误伤主路）。
func TestD474b_ApproveQualifiedPass(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	resp, err := s.ApproveCarryGraduation(context.Background(), approveReq())
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v errstr=%s", resp.Msg.Ok, err, resp.Msg.Error)
	}
	if f.combo == nil {
		t.Fatal("合格行应写白名单")
	}
}

// TestD474b_ApproveMT4Reject S4：mt_venue platform=0（MT4 退役）→ 拒写白名单。
func TestD474b_ApproveMT4Reject(t *testing.T) {
	f, s := gradFixture("long_cfd_short_perp")
	f.accts = []pgstore.BrokerAccount{{Venue: "1279967", Platform: 0}}
	resp, err := s.ApproveCarryGraduation(context.Background(), approveReq())
	if err != nil {
		t.Fatal(err)
	}
	if resp.Msg.Ok {
		t.Fatal("MT4 venue 不得批准")
	}
	if !strings.Contains(resp.Msg.Error, "platform=0") {
		t.Fatalf("error=%s", resp.Msg.Error)
	}
	if f.combo != nil {
		t.Fatal("MT4 拒批禁写白名单")
	}
}

// TestD488_Approve_SpotCarry_WritesCombo D-488：spot_carry 授权=组合进真金白名单
// （venue_buy=同所:spot × venue_sell=同所:perp，canonical 双腿同值；venue gap 如实透出）。
func TestD488_Approve_SpotCarry_WritesCombo(t *testing.T) {
	fee, entryB, lastB := 0.5, 10.0, 12.0
	f := &fakeGradDB{
		grad: &pgstore.CarryGraduation{ID: 7, PaperID: 44, Venue: "binance", Symbol: "SKLUSDT", Structure: "spot_carry", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 44, Venue: "binance", Symbol: "SKLUSDT", Structure: "spot_carry",
			Status: "active", AccruedUSD: 22.0, EntryTs: time.Now().Add(-6 * 24 * time.Hour),
			EstRoundTripFeeUSD: &fee, EntryBasisBps: &entryB, LastBasisBps: &lastB},
	}
	reload := &fakeReloader{}
	s := &Service{gradStore: f, xassetReloader: reload, xassetEnv: "real", allowVenuesSource: func(context.Context) (map[string]bool, error) {
		return map[string]bool{"binance:perp": true}, nil // spot 腿未放行→gap 如实透出
	}}
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(&v1.ApproveCarryGraduationRequest{GraduationId: 7, MaxLongQty: 10, MaxShortQty: 10, MaxGrossUsd: 500}))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("ok=%v err=%v errstr=%s", resp.Msg.Ok, err, resp.Msg.Error)
	}
	if f.combo == nil {
		t.Fatal("spot_carry approve 应写组合白名单（业主语义：授权即放行）")
	}
	if f.combo.VenueBuy != "binance:spot" || f.combo.VenueSell != "binance:perp" {
		t.Errorf("venue 对 = %s×%s, want binance:spot×binance:perp", f.combo.VenueBuy, f.combo.VenueSell)
	}
	if f.combo.CanonicalBuy != "SKLUSDT" || f.combo.CanonicalSell != "SKLUSDT" {
		t.Errorf("canonical 对 = %s×%s, want SKLUSDT×SKLUSDT", f.combo.CanonicalBuy, f.combo.CanonicalSell)
	}
	if resp.Msg.ComboId == nil {
		t.Error("combo_id 应回传")
	}
	if reload.calls == 0 {
		t.Error("白名单写入后应热重载")
	}
	// gap 序：venue 缺口在前，D-475a 敞口闸未设 venue 如实透出在后。
	if len(resp.Msg.VenueGaps) != 3 || resp.Msg.VenueGaps[0] != "binance:spot" {
		t.Errorf("venue_gaps = %v, want [binance:spot cap未设×2]", resp.Msg.VenueGaps)
	}
	if f.decision != "approved" {
		t.Errorf("落账 = %s, want approved", f.decision)
	}
}
