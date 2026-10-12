package dashboard

// D-540 断言锚：毕业授权可用基准/风险位/建议额三态。
// 覆盖：ok / insufficient（请求>基准→suggested=基准全额）/ liquidation
// （保证金估后池缓冲<1.5 安全垫→suggested=缓冲反推）+pct 可用基准口径+裸探针基准下发。

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
	"connectrpc.com/connect"
)

// d540Snaps 池快照（含 free_margin——D-540 可用基准源）。
func d540Snaps(eqMT, frMT, eqCEX, frCEX float64, ccy string) []pgstore.VenueAccountLatest {
	return []pgstore.VenueAccountLatest{
		{Venue: "1279967", VenueType: "mt5", EquityRaw: &eqMT, FreeMarginRaw: &frMT, Currency: ccy, FetchedAt: time.Now()},
		{Venue: "gate", VenueType: "perp", EquityRaw: &eqCEX, FreeMarginRaw: &frCEX, Currency: ccy, FetchedAt: time.Now()},
	}
}

func d540DryRun(t *testing.T, f *fakeGradDB, s *Service, req *v1.ApproveCarryGraduationRequest) *v1.ApproveCarryGraduationResponse {
	t.Helper()
	req.DryRun = true
	resp, err := s.ApproveCarryGraduation(context.Background(), connect.NewRequest(req))
	if err != nil || !resp.Msg.Ok {
		t.Fatalf("dry_run ok=%v err=%v errstr=%s", resp.Msg.Ok, err, resp.Msg.Error)
	}
	return resp.Msg
}

// TestD540_RiskOK 正常态：可用基准=min(free)=900；alloc200 → risk_level=ok+suggested=0。
func TestD540_RiskOK(t *testing.T) {
	f, s := d490Fixture(t, d540Snaps(2000, 2000, 1000, 900, "USD"), 0.01)
	resp := d540DryRun(t, f, s, &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingUsd: d490f(200)})
	if math.Abs(resp.AllocBaseUsd-900) > 1e-9 {
		t.Fatalf("alloc_base want 900（min free_margin）, got %v", resp.AllocBaseUsd)
	}
	if !strings.Contains(resp.AllocBaseNote, "free_margin") || strings.Contains(resp.AllocBaseNote, "净值回退") {
		t.Fatalf("基准注记应全 free_margin: %q", resp.AllocBaseNote)
	}
	if resp.RiskLevel != "ok" || resp.SuggestedAllocUsd != 0 {
		t.Fatalf("risk=%q suggested=%v want ok/0", resp.RiskLevel, resp.SuggestedAllocUsd)
	}
	if f.combo != nil {
		t.Error("dry_run 零副作用")
	}
}

// TestD540_RiskInsufficient 请求>可用基准→insufficient+suggested=基准全额。
func TestD540_RiskInsufficient(t *testing.T) {
	f, s := d490Fixture(t, d540Snaps(2000, 2000, 1000, 200, "USD"), 0.01)
	resp := d540DryRun(t, f, s, &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingUsd: d490f(300)})
	if resp.RiskLevel != "insufficient" {
		t.Fatalf("risk=%q want insufficient（alloc300>base200）", resp.RiskLevel)
	}
	if math.Abs(resp.SuggestedAllocUsd-200) > 1e-9 {
		t.Fatalf("suggested want 200（基准全额）, got %v", resp.SuggestedAllocUsd)
	}
	if len(resp.RiskReasons) == 0 || !strings.Contains(resp.RiskReasons[0], "可用基准") {
		t.Fatalf("risk_reasons 缺基准理由: %v", resp.RiskReasons)
	}
}

// TestD540_RiskLiquidation 保证金估后池缓冲比<1.5→liquidation+suggested=缓冲反推。
// gate:perp eq200/free50（已占 150）：alloc50→腿名义25×0.2=保证金5→headroom=200/155=1.29<1.5。
// D-565 S2：venue 杠杆实读——夹具钉 5x（frac=0.2 同原口径）。
func TestD540_RiskLiquidation(t *testing.T) {
	f, s := d490Fixture(t, d540Snaps(2000, 2000, 200, 50, "USD"), 0.01)
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 5, nil }
	resp := d540DryRun(t, f, s, &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingUsd: d490f(50)})
	if resp.RiskLevel != "liquidation" {
		t.Fatalf("risk=%q want liquidation（headroom1.29<1.5）", resp.RiskLevel)
	}
	if resp.SuggestedAllocUsd != 0 {
		t.Fatalf("缓冲反推为负→suggested 应 0, got %v", resp.SuggestedAllocUsd)
	}
	if len(resp.RiskReasons) == 0 || !strings.Contains(strings.Join(resp.RiskReasons, " "), "安全垫") {
		t.Fatalf("risk_reasons 缺安全垫理由: %v", resp.RiskReasons)
	}
}

// TestD540_PctUsesAllocBase pct 口径=可用基准（业主令）：pct0.5×base800=alloc400
// （旧净值口径会算 0.5×1000=500——钉死基准切换）。est_alloc_pct 回显同基准。
func TestD540_PctUsesAllocBase(t *testing.T) {
	f, s := d490Fixture(t, d540Snaps(2000, 2000, 1000, 800, "USD"), 0.01)
	resp := d540DryRun(t, f, s, &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingPct: d490f(0.5)})
	if math.Abs(resp.EstAllocUsd-400) > 1e-9 || math.Abs(resp.EstAllocPct-0.5) > 1e-9 {
		t.Fatalf("pct 应乘可用基准800: alloc=%v pct=%v（want 400/0.5）", resp.EstAllocUsd, resp.EstAllocPct)
	}
}

// TestD540_ProbeBase 裸探针（无 sizing 入参）也回可用基准——前端免输入即显基准行。
func TestD540_ProbeBase(t *testing.T) {
	f, s := d490Fixture(t, d540Snaps(2000, 1500, 1000, 700, "USD"), 0.01)
	resp := d540DryRun(t, f, s, &v1.ApproveCarryGraduationRequest{GraduationId: 5})
	if math.Abs(resp.AllocBaseUsd-700) > 1e-9 || !strings.Contains(resp.AllocBaseNote, "gate:perp") {
		t.Fatalf("裸探针可用基准 want 700+gate:perp 注记, got %v %q", resp.AllocBaseUsd, resp.AllocBaseNote)
	}
}

// TestD540_BaseFallbackEquity 实况可用缺 free → 基准回退净值（实拉语义下 note 标「实拉」）。
func TestD540_BaseFallbackEquity(t *testing.T) {
	f, s := d490Fixture(t, d490Snaps(2000, 1000, "USD", time.Minute), 0.01) // 无 free
	resp := d540DryRun(t, f, s, &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingUsd: d490f(200)})
	if math.Abs(resp.AllocBaseUsd-1000) > 1e-9 {
		t.Fatalf("缺 free 基准应回退净值 1000, got %v", resp.AllocBaseUsd)
	}
	if !strings.Contains(resp.AllocBaseNote, "实拉") {
		t.Fatalf("实况路径注记应标实拉: %q", resp.AllocBaseNote)
	}
}
