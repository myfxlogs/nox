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
	// 498 真实形状锚（返修2）：Qty=1000 张（venue 请求单位）vs FilledQty=1（base 口径）——
	// 聚合对拍必须用 Qty（venue 单位），用 EffectiveQty 会取 FilledQty=1 必误报。
	leg := mkOpenLeg(498, 0, "okx:perp", "ordX", "SELL", "XAU-USDT-SWAP", 1000)
	one := 1.0
	leg.FilledQty = &one
	fs.legs[498] = []*pgstore.PositionLeg{leg}
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

// B2-3 超容差：local 1000 vs venue 1200（diff 20% > 0.5% tol）。
// 单轮瞬读只计 pending 零告警，连跑 3 轮后 critical 恰一条（episode 单发），
// mm=已确认口径（前两轮 pending 观察不计）。
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

	mm1, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run 1: %v", err)
	}
	mm2, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run 2: %v", err)
	}
	mm3, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run 3: %v", err)
	}
	if mm1 != 0 || mm2 != 0 {
		t.Errorf("前两轮 pending 观察不计 mismatches，got %d/%d", mm1, mm2)
	}
	if mm3 != 1 {
		t.Fatalf("第 3 轮（确认轮）mismatches=%d, want 1", mm3)
	}
	if !hasAlert(fs, "critical", "CEX 聚合持仓对拍不一致") {
		t.Fatalf("缺少聚合对拍 critical，alerts=%v", fs.alerts)
	}
	if n := countAggCrit(fs); n != 1 {
		t.Errorf("episode 内 critical 应恰 1 条，got %d (alerts=%v)", n, fs.alerts)
	}
	if !hasAlert(fs, "critical", "498") {
		t.Errorf("critical 应含贡献腿 posID=498，alerts=%v", fs.alerts)
	}
}

// B2-4 本地有 venue 无（venue 返回空持仓）→ 连续 3 轮确认后 critical（venue=0 属超 tol 分支）。
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

	for i := 0; i < 2; i++ {
		mm, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("Run %d: %v", i+1, err)
		}
		if mm != 0 {
			t.Errorf("第 %d 轮 pending 观察不计 mismatches，got %d", i+1, mm)
		}
	}
	mm, err := r.Run(context.Background())
	if err != nil {
		t.Fatalf("Run 3: %v", err)
	}
	if mm != 1 {
		t.Fatalf("第 3 轮 mismatches=%d, want 1（venue 无仓=连续确认后 critical）", mm)
	}
	if n := countAggCrit(fs); n != 1 {
		t.Errorf("episode 内 critical 应恰 1 条，got %d (alerts=%v)", n, fs.alerts)
	}
}

// countAggCrit 统计 CEX 聚合对拍 critical 条数（episode 单发断言用）。
func countAggCrit(fs *fakeStore) int {
	n := 0
	for _, a := range fs.alerts {
		if a.level == "critical" && contains(a.msg, "CEX 聚合持仓对拍不一致") {
			n++
		}
	}
	return n
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

// —— D-401：CEX 聚合对拍瞬读二次确认 ——

// deadOKXQuerier 返回指向已关闭 httptest.Server 的 querier（连接拒绝=venue 读失败）。
func deadOKXQuerier(t *testing.T) *CEXPositionQuerier {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close()
	return NewCEXPositionQuerier("okx", "perp", "k", "s", "p", srv.URL, false)
}

// d401Fixture 本地 498 OPEN 仓 + okx:perp SELL 1000 张腿（D-401 各用例共用）。
func d401Fixture(fs *fakeStore) {
	fs.positions[498] = mkPosition(498, "OPEN", "CROSS_VENUE", time.Now().Add(-time.Hour))
	fs.legs[498] = []*pgstore.PositionLeg{
		mkOpenLeg(498, 0, "okx:perp", "ordX", "SELL", "XAU-USDT-SWAP", 1000),
	}
}

// S5① 失败读抑制：querier 返 error=未知≠零读——3 轮全零 mismatch 零 critical。
// 旧代码：venue 查询失败 continue → cexVenue 零行 → local=1000 vs venue=0 critical ×3 → 红。
func TestD401_VenueReadFail_Suppressed(t *testing.T) {
	fs := newFakeStore()
	d401Fixture(fs)
	r := New(fs, deadOKXQuerier(t))
	r.runTimeout = 5 * time.Second

	for i := 0; i < 3; i++ {
		mm, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("Run %d: %v", i+1, err)
		}
		if mm != 0 {
			t.Errorf("第 %d 轮读失败轮 mismatches=%d, want 0（未知≠零读）", i+1, mm)
		}
	}
	if len(fs.alerts) != 0 {
		t.Errorf("读失败轮不应有任何告警，alerts=%v", fs.alerts)
	}
}

// S5② 连续 2 轮不一致 → 未达确认阈值，零 critical 零 mismatch。
func TestD401_TwoRounds_NoCritical(t *testing.T) {
	srv := okxPosStub(t, "XAU-USDT-SWAP", "short", "1200")
	defer srv.Close()
	fs := newFakeStore()
	d401Fixture(fs)
	r := New(fs, okxQuerier(srv))
	r.runTimeout = 5 * time.Second

	for i := 0; i < 2; i++ {
		mm, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("Run %d: %v", i+1, err)
		}
		if mm != 0 {
			t.Errorf("第 %d 轮 mismatches=%d, want 0（pending 观察）", i+1, mm)
		}
	}
	if n := countAggCrit(fs); n != 0 {
		t.Errorf("2 轮不应 critical，got %d (alerts=%v)", n, fs.alerts)
	}
}

// S5③ 第 4 轮仍不一致 → 不新增 critical（==3 恰一次，episode 内单发）；
// 第 4 轮 mm=0（已确认口径：>3 不再计入）。
func TestD401_FourthRound_NoNewCritical(t *testing.T) {
	srv := okxPosStub(t, "XAU-USDT-SWAP", "short", "1200")
	defer srv.Close()
	fs := newFakeStore()
	d401Fixture(fs)
	r := New(fs, okxQuerier(srv))
	r.runTimeout = 5 * time.Second

	for i := 0; i < 4; i++ {
		if _, err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run %d: %v", i+1, err)
		}
		if i == 2 { // 第 3 轮：确认阈值，critical 恰 1 条
			if n := countAggCrit(fs); n != 1 {
				t.Fatalf("第 3 轮应发 1 条 critical，got %d", n)
			}
		}
	}
	if n := countAggCrit(fs); n != 1 {
		t.Errorf("第 4 轮不应新增 critical（episode 单发），got %d (alerts=%v)", n, fs.alerts)
	}
}

// S5④ 第 3 轮告警 → 第 4 轮恢复（pending 清零）→ 再不一致 2 轮 → 仍零新增 critical
// （episode 已闭合，新 episode 重新计数）。
func TestD401_EpisodeRecovery_Recount(t *testing.T) {
	pos := "1200" // 闭包可变 venue 持仓（Run 同步串行，无并发）
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		data := "[]"
		if pos != "" {
			data = fmt.Sprintf(`[{"instId":"XAU-USDT-SWAP","posSide":"short","pos":%q,"avgPx":"2400","upl":"0","fee":"0","cTime":"1788000000000","margin":"0","imr":"0","mmr":"0"}]`, pos)
		}
		fmt.Fprintf(w, `{"code":"0","msg":"","data":%s}`, data)
	}))
	defer srv.Close()
	fs := newFakeStore()
	d401Fixture(fs)
	r := New(fs, okxQuerier(srv))
	r.runTimeout = 5 * time.Second

	for i := 0; i < 3; i++ { // 3 轮不一致 → 第 3 轮 critical
		if _, err := r.Run(context.Background()); err != nil {
			t.Fatalf("Run %d: %v", i+1, err)
		}
	}
	if n := countAggCrit(fs); n != 1 {
		t.Fatalf("第 3 轮应发 1 条 critical，got %d", n)
	}
	pos = "1000" // 恢复：匹配 → pending 清零
	if mm, err := r.Run(context.Background()); err != nil || mm != 0 {
		t.Fatalf("恢复轮 mm=%d err=%v, want 0/nil", mm, err)
	}
	pos = "1200" // 新 episode：再 2 轮不一致（未达阈值 3）
	for i := 0; i < 2; i++ {
		mm, err := r.Run(context.Background())
		if err != nil {
			t.Fatalf("新 episode Run %d: %v", i+1, err)
		}
		if mm != 0 {
			t.Errorf("新 episode 第 %d 轮 mm=%d, want 0（重新计数）", i+1, mm)
		}
	}
	if n := countAggCrit(fs); n != 1 {
		t.Errorf("episode 闭合后重新计数，不应新增 critical，got %d (alerts=%v)", n, fs.alerts)
	}
}
