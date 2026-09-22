// d385_test.go —— D-385 S1 验收锚 B2：CEX 聚合持仓对拍。
// fixture 复刻真实 venue 形态（KB regression-anchor-encodes-bug）：用真 *CEXPositionQuerier
// + httptest 服务端（okx positions 响应同形），本地腿 venue="okx:perp"、symbol=instId、
// side=BUY/SELL、qty=张（与 venue pos.Pos 同单位）。
package reconcile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// okxPosStub 回固定 SWAP 持仓的 okx positions mock（pos 单位=张，posSide=long/short/net）。
func okxPosStub(t *testing.T, instID, posSide, pos string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/account/positions" {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		data := "[]"
		if pos != "" {
			data = fmt.Sprintf(`[{"instId":%q,"posSide":%q,"pos":%q,"avgPx":"2400","upl":"0","fee":"0","cTime":"1788000000000","margin":"0","imr":"0","mmr":"0"}]`, instID, posSide, pos)
		}
		fmt.Fprintf(w, `{"code":"0","msg":"","data":%s}`, data)
	}))
}

func okxQuerier(srv *httptest.Server) *CEXPositionQuerier {
	return NewCEXPositionQuerier("okx", "perp", "k", "s", "p", srv.URL, false)
}

// B2-1 匹配：本地 FILLED 腿 1000 张 SELL + venue short 1000 → 零告警零 mismatch，
// 且 CEX 腿不进逐单路径（无「查证器未就绪」刷屏的告警面——mm==0 即证）。
func TestD385_CEXAgg_Matched(t *testing.T) {
	srv := okxPosStub(t, "XAU-USDT-SWAP", "short", "1000")
	defer srv.Close()
	fs := newFakeStore()
	fs.positions[498] = mkPosition(498, "OPEN", "CROSS_VENUE", time.Now().Add(-time.Hour))
	fs.legs[498] = []*pgstore.PositionLeg{
		mkOpenLeg(498, 0, "okx:perp", "ordX", "SELL", "XAU-USDT-SWAP", 1000),
	}
	r := New(fs, okxQuerier(srv))
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 0 {
		t.Errorf("mismatches=%d, want 0（聚合量匹配）", mm)
	}
	if len(fs.alerts) != 0 {
		t.Errorf("alerts=%v, want none", fs.alerts)
	}
}

// B2-2 venue-only：本地无腿 + venue 持仓 → 「系统外单不追踪」语义保留，零告警零 mismatch。
func TestD385_CEXAgg_VenueOnly_ExternalUntracked(t *testing.T) {
	srv := okxPosStub(t, "XAU-USDT-SWAP", "long", "500")
	defer srv.Close()
	fs := newFakeStore()
	r := New(fs, okxQuerier(srv))
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 0 {
		t.Errorf("mismatches=%d, want 0（系统外单不计不一致）", mm)
	}
	if hasAlert(fs, "critical", "对拍不一致") {
		t.Errorf("venue-only 不应告警，alerts=%v", fs.alerts)
	}
}

// B2-3 超容差：local 1000 vs venue 1200（diff 20% > 0.5% tol）→ critical 含贡献腿 posID。
func TestD385_CEXAgg_Mismatch_Alerts(t *testing.T) {
	srv := okxPosStub(t, "XAU-USDT-SWAP", "short", "1200")
	defer srv.Close()
	fs := newFakeStore()
	fs.positions[498] = mkPosition(498, "OPEN", "CROSS_VENUE", time.Now().Add(-time.Hour))
	fs.legs[498] = []*pgstore.PositionLeg{
		mkOpenLeg(498, 0, "okx:perp", "ordX", "SELL", "XAU-USDT-SWAP", 1000),
	}
	r := New(fs, okxQuerier(srv))
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 1 {
		t.Fatalf("mismatches=%d, want 1", mm)
	}
	if !hasAlert(fs, "critical", "CEX 聚合持仓对拍不一致") {
		t.Fatalf("缺少聚合对拍 critical，alerts=%v", fs.alerts)
	}
	if !hasAlert(fs, "critical", "498") {
		t.Errorf("critical 应含贡献腿 posID=498，alerts=%v", fs.alerts)
	}
}

// B2-4 本地有 venue 无（venue 返回空持仓）→ critical（venue=0 属超 tol 分支）。
func TestD385_CEXAgg_LocalOnly_Alerts(t *testing.T) {
	srv := okxPosStub(t, "", "", "")
	defer srv.Close()
	fs := newFakeStore()
	fs.positions[498] = mkPosition(498, "OPEN", "CROSS_VENUE", time.Now().Add(-time.Hour))
	fs.legs[498] = []*pgstore.PositionLeg{
		mkOpenLeg(498, 0, "okx:perp", "ordX", "SELL", "XAU-USDT-SWAP", 1000),
	}
	r := New(fs, okxQuerier(srv))
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 1 {
		t.Fatalf("mismatches=%d, want 1（venue 无仓=critical）", mm)
	}
	if !hasAlert(fs, "critical", "CEX 聚合持仓对拍不一致") {
		t.Errorf("缺少聚合对拍 critical，alerts=%v", fs.alerts)
	}
}

// B2-5 容差内：local 1000 vs venue 1002（diff 0.2% < 0.5% tol）→ 静默匹配。
func TestD385_CEXAgg_WithinTolerance(t *testing.T) {
	srv := okxPosStub(t, "XAU-USDT-SWAP", "short", "1002")
	defer srv.Close()
	fs := newFakeStore()
	fs.positions[498] = mkPosition(498, "OPEN", "CROSS_VENUE", time.Now().Add(-time.Hour))
	fs.legs[498] = []*pgstore.PositionLeg{
		mkOpenLeg(498, 0, "okx:perp", "ordX", "SELL", "XAU-USDT-SWAP", 1000),
	}
	r := New(fs, okxQuerier(srv))
	r.runTimeout = 5 * time.Second

	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if mm != 0 {
		t.Errorf("mismatches=%d, want 0（容差内）", mm)
	}
}
