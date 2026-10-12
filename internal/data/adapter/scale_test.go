// d594_scale_test.go D-594 T 组：报价单位归一（emitPlan/发射口乘价/未标定抑制/swap 原生价）。
package adapter

import (
	"context"
	"math"
	"testing"
	"time"

	"google.golang.org/grpc"
	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"
)

// TestD594EmitPlan 表驱动：三尺度×scale 出 canonical lb 价系数 / 未标定行抑制 /
// emitBlocked 原生帧不乘价 / 原生键与敏感 canonical 同名冲突时整帧丢弃。
func TestD594EmitPlan(t *testing.T) {
	a := newQuoteTestAdapter(&fakeMT5Client{}, &fakeConnMgr{})
	a.SetSymbolMap(map[string]string{
		"COPPER":  "XCUUSD", // 1279967 $/吨
		"XCUUSDm": "XCUUSD", // 39050818 ¢/磅（行内 scale 示意）
		"Copper":  "XCUUSD", // 61585565 $/磅
		"COPPERu": "XCUUSD", // 未标定行（DB 行存在但 scale NULL → 不带 scale）
		"COPPERv": "XCUUSD", // emitBlocked 变体
	})
	a.SetSymbolScales(map[string]SymbolScale{
		"COPPER":  {PriceScale: 1.0 / 2204.62, QtyScale: 2204.62},
		"XCUUSDm": {PriceScale: 0.01, QtyScale: 250},
		"Copper":  {PriceScale: 1.0, QtyScale: 2000},
		"COPPERv": {PriceScale: 1.0 / 2204.62, QtyScale: 2204.62},
	})
	a.SetUnitSensitive(map[string]bool{"XCUUSD": true})
	a.SetCanonicalEmitBlocked(map[string]bool{"COPPERv": true})

	for _, tc := range []struct {
		name      string
		brokerSym string
		wantSym   string
		wantScale float64
		wantEmit  bool
	}{
		{"ton→lb", "COPPER", "XCUUSD", 1.0 / 2204.62, true},
		{"cents→lb", "XCUUSDm", "XCUUSD", 0.01, true},
		{"lb identity", "Copper", "XCUUSD", 1.0, true},
		{"未标定敏感行降 native", "COPPERu", "COPPERu", 1.0, true},
		{"blocked 变体降 native 不乘价", "COPPERv", "COPPERv", 1.0, true},
		{"非敏感品种未标定照常 canonical×1", "EURUSD", "EURUSD", 1.0, true},
	} {
		sym, ps, emit := a.emitPlan(tc.brokerSym)
		if emit != tc.wantEmit || sym != tc.wantSym || math.Abs(ps-tc.wantScale) > 1e-12 {
			t.Errorf("%s: emitPlan(%q)=(%q,%v,%v), want (%q,%v,%v)",
				tc.name, tc.brokerSym, sym, ps, emit, tc.wantSym, tc.wantScale, tc.wantEmit)
		}
	}

	// 原生键=敏感 canonical 名且未标定（identity 兜底路径，39050818 型污染源头）→ 整帧丢弃。
	if sym, _, emit := a.emitPlan("XCUUSD"); emit || sym != "" {
		t.Errorf("identity XCUUSD unrated: emitPlan=(%q,%v), want dropped", sym, emit)
	}
}

// TestD594QuoteStreamScalesCanonical 端到端：帧经 consumeQuoteFrames 后 canonical 键乘价、
// native/抑制帧保持原生价。
func TestD594QuoteStreamScalesCanonical(t *testing.T) {
	client := &fakeMT5Client{}
	frames := make(chan *mt5pb.OnQuoteReply, 2)
	frames <- &mt5pb.OnQuoteReply{Result: &mt5pb.Quote{Symbol: "COPPER", Bid: 14500, Ask: 14510}}
	frames <- &mt5pb.OnQuoteReply{Result: &mt5pb.Quote{Symbol: "COPPERu", Bid: 700, Ask: 701}}
	stream := newFakeQuoteStream(context.Background(), frames, nil)
	client.onQuoteStreams = []grpc.ServerStreamingClient[mt5pb.OnQuoteReply]{stream}

	a := newQuoteTestAdapter(client, &fakeConnMgr{})
	a.SetSymbolMap(map[string]string{"COPPER": "XCUUSD", "COPPERu": "XCUUSD"})
	a.SetSymbolScales(map[string]SymbolScale{
		"COPPER": {PriceScale: 1.0 / 2204.62, QtyScale: 2204.62},
	})
	a.SetUnitSensitive(map[string]bool{"XCUUSD": true})
	ch, cleanup := startQuoteStream(t, a, []string{"COPPER", "COPPERu"})
	defer cleanup()

	got := map[string][2]float64{}
	for i := 0; i < 2; i++ {
		select {
		case q := <-ch:
			got[q.Symbol] = [2]float64{q.Bid, q.Ask}
		case <-time.After(3 * time.Second):
			t.Fatalf("quote %d not received", i)
		}
	}
	// 14500 $/ton ÷ 2204.62 ≈ 6.5771 $/lb（±0.5% 验收带内）。
	if b := got["XCUUSD"][0]; math.Abs(b-14500.0/2204.62) > 1e-6 {
		t.Errorf("ton venue canonical bid=%v, want ~%.4f", b, 14500.0/2204.62)
	}
	if got["COPPERu"][0] != 700 {
		t.Errorf("unrated native frame bid=%v, want raw 700 under native key", got["COPPERu"][0])
	}
}
