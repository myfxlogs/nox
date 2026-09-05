// D-234 CMC 看盘实时化测试：1s 蓄压合并 + 同源公式镜像恒和=0 + topic 扇出 + 源文本断言。
package dashboard

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/collect/cmc"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
)

// TestCMCWatchTopicPublishSubscribe D-234：cmcWatchTopic Publish → 订阅者收到 *CMCWatchUpdate（case 13 数据源）。
func TestCMCWatchTopicPublishSubscribe(t *testing.T) {
	topic := bus.NewTopic[*v1.CMCWatchUpdate]()
	ch, cancel := topic.Subscribe()
	defer cancel()

	want := &v1.CMCWatchUpdate{Mid: 0.3236, Bid: 0.3228, Ask: 0.3244}
	topic.Publish(want)

	select {
	case got := <-ch:
		if got.Mid != want.Mid || got.Bid != want.Bid || got.Ask != want.Ask {
			t.Fatalf("round-trip mismatch: got %+v want %+v", got, want)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for published CMCWatchUpdate")
	}
}

// TestCMCWatchTopicNilSafe D-234：Service.cmcWatchTopic nil → subscribeCMCWatchTopic 返回 nil channel + noop cancel。
func TestCMCWatchTopicNilSafe(t *testing.T) {
	s := &Service{}
	if s.cmcWatchTopic != nil {
		t.Fatal("cmcWatchTopic should be nil by default")
	}
	ch, cancel := s.subscribeCMCWatchTopic()
	defer cancel()
	if ch != nil {
		t.Fatalf("nil topic: channel = %v, want nil", ch)
	}
}

// TestNotifyCMCFrameCoalesce D-234：1s 蓄压——窗口内多帧合并为一次 flush（≤1 条/秒）。
// 测试注 10ms 蓄压间隔加速；连续 5 次 NotifyCMCFrame → 恰好 1 次 Publish。
// 用 hedgeTestStore（空表）使 flushCMCWatch→computeCMCWatchUpdate 不 panic（空表容忍返回零值载荷）。
func TestNotifyCMCFrameCoalesce(t *testing.T) {
	st := hedgeTestStore(t)
	s := &Service{store: st, freshTTL: time.Minute}
	topic := bus.NewTopic[*v1.CMCWatchUpdate]()
	s.SetCMCWatchTopic(topic)
	s.SetCMCWatchFlushInterval(10 * time.Millisecond)

	ch, cancel := topic.Subscribe()
	defer cancel()

	// 连续 5 次通知（应合并为 1 次 flush）
	for i := 0; i < 5; i++ {
		s.NotifyCMCFrame(cmc.FramePrice)
	}

	// 等蓄压窗口到期 + flush（10ms 窗口 + flush）
	var count int32
	go func() {
		for range ch {
			atomic.AddInt32(&count, 1)
		}
	}()

	time.Sleep(300 * time.Millisecond) // 10ms 窗口已远超，flush 应已发生且仅 1 次

	got := atomic.LoadInt32(&count)
	if got != 1 {
		t.Fatalf("coalesce: received %d publishes, want 1 (5 frames in one 10ms window)", got)
	}
}

// TestComputeCMCWatchUpdate_MirrorPnlSumZero D-234：同源公式——双腿 PnL 镜像恒和=0 + bid/ask/mid + 强平净值。
// 构造 TRX 账户 + CMC 持仓（entry=0.33171, evalBid/evalAsk）+ CMC price（mid=0.3236, ask=0.32335）+ 账户摘要。
func TestComputeCMCWatchUpdate_MirrorPnlSumZero(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	now := time.Now().UTC()

	// CMC 持仓（空单 18100，entry=0.33171，evalBid=0.32285, evalAsk=0.32335）
	if err := s.InsertCMCPositions(ctx, 1, []pgstore.CMCPosition{{
		AccountID: 1, InstrumentID: "X-AFQUL", Direction: "SELL", Quantity: 18100,
		EntryPrice: 0.33171, UnrealisedPnL: -117.83, MarginPercentage: 0.1,
		EvaluationBidPrice: 0.32285, EvaluationAskPrice: 0.32335, FetchedAt: now,
	}}); err != nil {
		t.Fatalf("InsertCMCPositions: %v", err)
	}
	// CMC price（mid=0.3236 REST 行）
	if err := s.InsertCMCPrice(ctx, pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", MidPrice: 0.3236, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertCMCPrice: %v", err)
	}
	// CMC WS ask 行（ForSide Sell 取 WS ask=0.32335）
	if err := s.InsertCMCPrice(ctx, pgstore.CMCPrice{
		InstrumentID: "X-AFQUL", AskPrice: 0.32335, Src: "cmc_ws", FetchedAt: now.Add(time.Second),
	}); err != nil {
		t.Fatalf("InsertCMCPrice ws: %v", err)
	}
	// CMC 账户摘要（usedMargin=584.81 → 强平净值=292.405, 预警=467.848）
	if err := s.InsertCMCAccountSummary(ctx, pgstore.CMCAccountSummary{
		AccountID: 1, TotalAccountValue: 2169.02, TotalCashBalance: 2026.76,
		TotalEquityUsedInMargin: 584.81, CloseoutPrice: 0.4226,
		DistanceToCloseoutPct: 29.8, FetchedAt: now,
	}); err != nil {
		t.Fatalf("InsertCMCAccountSummary: %v", err)
	}

	svc := &Service{store: s, freshTTL: time.Minute, liqMMR: 0.005}
	upd := svc.computeCMCWatchUpdate(ctx)

	// mid=0.3236（REST mid）
	if upd.Mid != 0.3236 {
		t.Errorf("mid = %v, want 0.3236", upd.Mid)
	}
	// ask=0.32335（WS ask 优先）
	if upd.Ask != 0.32335 {
		t.Errorf("ask = %v, want 0.32335 (WS ask priority)", upd.Ask)
	}
	// bid=0.32285（持仓 EvaluationBidPrice）
	if upd.Bid != 0.32285 {
		t.Errorf("bid = %v, want 0.32285 (position EvaluationBidPrice)", upd.Bid)
	}
	// baseline=0.33171
	if upd.BaselinePrice != 0.33171 {
		t.Errorf("baseline = %v, want 0.33171", upd.BaselinePrice)
	}
	// short_qty=18100, lev=10
	if upd.ShortQty != 18100 {
		t.Errorf("short_qty = %v, want 18100", upd.ShortQty)
	}
	if upd.ShortLev != 10 {
		t.Errorf("short_lev = %v, want 10 (1/0.1)", upd.ShortLev)
	}
	// 镜像恒和=0：pnlPx=evalAsk=0.32335（WS ask 优先，非 0 不回退 mid）
	// spot_pnl = (0.32335 - 0.33171) × 18100 = -151.396
	// short_pnl = (0.33171 - 0.32335) × 18100 = 151.396
	sum := upd.LegSpotPnl + upd.LegShortPnl
	if sum > 1e-9 || sum < -1e-9 {
		t.Errorf("镜像恒和 = %v, want 0 (spot %v + short %v)", sum, upd.LegSpotPnl, upd.LegShortPnl)
	}
	// 强平/预警净值（platform_rule_50）
	if diff := upd.CloseoutLevelUsd - 584.81*0.5; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("closeout_level_usd = %v, want %v", upd.CloseoutLevelUsd, 584.81*0.5)
	}
	if diff := upd.WarningLevelUsd - 584.81*0.8; diff > 1e-9 || diff < -1e-9 {
		t.Errorf("warning_level_usd = %v, want %v", upd.WarningLevelUsd, 584.81*0.8)
	}
	// 账户摘要
	if upd.CmcCash != 2026.76 {
		t.Errorf("cmc_cash = %v, want 2026.76", upd.CmcCash)
	}
	if upd.CmcEquity != 2169.02 {
		t.Errorf("cmc_equity = %v, want 2169.02", upd.CmcEquity)
	}
	if upd.UsedMargin != 584.81 {
		t.Errorf("used_margin = %v, want 584.81", upd.UsedMargin)
	}
	// closeout_source 标定
	if upd.CloseoutSource != "platform_rule_50" {
		t.Errorf("closeout_source = %q, want platform_rule_50", upd.CloseoutSource)
	}
	// ws_connected 未接线 → false
	if upd.WsConnected {
		t.Error("ws_connected = true, want false (未接线)")
	}
}

// TestComputeCMCWatchUpdate_EmptyTolerant D-234：空表 → 零值载荷不 panic（空表容忍）。
func TestComputeCMCWatchUpdate_EmptyTolerant(t *testing.T) {
	s := hedgeTestStore(t)
	ctx := context.Background()
	svc := &Service{store: s, freshTTL: time.Minute}
	upd := svc.computeCMCWatchUpdate(ctx)
	if upd == nil {
		t.Fatal("update = nil, want non-nil (空表容忍)")
	}
	if upd.Mid != 0 {
		t.Errorf("mid = %v, want 0 (空表)", upd.Mid)
	}
}

// TestCMCWatchPanelSourceAssert D-234 R-U2：R-U 端态防回归断言（面板已删，数据管线保留）。
// 验证：①CMCWatchPanel.tsx 不存在 ②16-cmc-watch.css 不存在 ③useSnapshot.ts 含 case "cmcUpdate" 合并进 hedgeStructure。
func TestCMCWatchPanelSourceAssert(t *testing.T) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	webSrc := filepath.Join(filepath.Dir(testFile), "..", "..", "web", "src")

	// ① CMCWatchPanel.tsx 不存在（R-U 还原：面板已删）
	panelPath := filepath.Join(webSrc, "components", "CMCWatchPanel.tsx")
	if _, err := os.Stat(panelPath); !os.IsNotExist(err) {
		t.Fatalf("CMCWatchPanel.tsx should not exist (R-U 还原), stat err=%v", err)
	}

	// ② 16-cmc-watch.css 不存在（R-U 还原：样式已删）
	cssPath := filepath.Join(webSrc, "styles", "16-cmc-watch.css")
	if _, err := os.Stat(cssPath); !os.IsNotExist(err) {
		t.Fatalf("16-cmc-watch.css should not exist (R-U 还原), stat err=%v", err)
	}

	// ③ useSnapshot.ts 含 case "cmcUpdate" 合并进 hedgeStructure（数据管线保留，数据复用对冲卡实时化）
	snapPath := filepath.Join(webSrc, "hooks", "useSnapshot.ts")
	snapData, err := os.ReadFile(snapPath)
	if err != nil {
		t.Fatalf("read useSnapshot: %v", err)
	}
	snapSrc := string(snapData)
	if !strings.Contains(snapSrc, `u.case === "cmcUpdate"`) {
		t.Fatal("useSnapshot missing case cmcUpdate branch (数据管线应保留)")
	}
	if !strings.Contains(snapSrc, "hedgeStructure") {
		t.Fatal("useSnapshot cmcUpdate branch missing hedgeStructure merge (R-U: 字段级合并进对冲卡)")
	}
}
