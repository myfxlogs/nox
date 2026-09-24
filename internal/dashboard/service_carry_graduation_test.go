// service_carry_graduation_test.go —— D-403 S6：approve 全分支（fake store+fake reloader+fake automap）。
// 覆盖：combo 插入字段正确（双 structure 方向）/pending 守卫/caps 必填/venue gap 命中
// （1279967:mt5 型）/非 cfd 体检分支不写白名单/reject 流转/唯一键撞/reloader 未接线 fail-closed。
package dashboard

import (
	"context"
	"fmt"
	"testing"

	"arb/internal/exec"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
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
	return nil, nil
}

func (f *fakeGradDB) LoadXAssetAllowCombos(_ context.Context, environment string) ([]pgstore.XAssetAllowRow, error) {
	return []pgstore.XAssetAllowRow{{
		CanonicalBuy: "BTCUSDT", CanonicalSell: "BTCUSD", VenueBuy: "gate:perp", VenueSell: "1279967:mt5",
		MaxLongQty: 1000, MaxShortQty: 0.01, MaxGrossUSD: 5000,
	}}, nil
}

type fakeReloader struct{ calls int }

func (f *fakeReloader) ReloadXAssetAllowCombos(context.Context) error { f.calls++; return nil }

func gradFixture(structure string) (*fakeGradDB, *Service) {
	mt := "1279967"
	f := &fakeGradDB{
		grad:  &pgstore.CarryGraduation{ID: 5, PaperID: 42, Venue: "1279967×gate", Symbol: "BTC", Structure: structure, MtVenue: &mt, Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 42, Venue: "1279967×gate", Symbol: "BTC", Structure: structure},
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
	// venue gap：1279967:mt5 不在 env 快照 → 命中报告
	if len(resp.Msg.VenueGaps) != 1 || resp.Msg.VenueGaps[0] != "1279967:mt5" {
		t.Errorf("venue_gaps got %v, want [1279967:mt5]", resp.Msg.VenueGaps)
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
	f := &fakeGradDB{
		grad:  &pgstore.CarryGraduation{ID: 6, PaperID: 43, Venue: "gate×okx", Symbol: "BTC", Structure: "perp_perp", Status: "pending"},
		paper: &pgstore.PaperSpotCarry{ID: 43, Venue: "gate×okx", Structure: "perp_perp"},
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
