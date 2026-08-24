// mt_swap_margin_rate_test.go —— S6（2026-08-24）：保证金预付款率采集。
// 验证 SwapFacts 从 SymGroup.InitMarginRate/MntnMarginRate 提取首档入 fact，
// 缺字段不发；跟随 D-060 tradeModeFact 先例走 facts 通道。
package adapter

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// TestSwapFacts_MarginRateFirstTier 多档 margin rate 取首档入 fact。
func TestSwapFacts_MarginRateFirstTier(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v6", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			// 多档 margin rate：首档 0.01（1%），二档 0.02，三档 0.05。
			InitMarginRate: []float64{0.01, 0.02, 0.05},
			MntnMarginRate: []float64{0.005, 0.01, 0.025},
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
	}}
	a := &MT5Adapter{venue: "v6", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD"})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}

	// init_margin_rate fact：首档 0.01。
	var initFact *fact.Fact
	var mntnFact *fact.Fact
	for i := range fs {
		switch fs[i].Kind {
		case fact.KindInitMarginRate:
			initFact = &fs[i]
		case fact.KindMntnMarginRate:
			mntnFact = &fs[i]
		}
	}
	if initFact == nil {
		t.Fatal("init_margin_rate fact missing")
	}
	if initFact.Value != 0.01 {
		t.Errorf("init_margin_rate value = %v, want 0.01 (first tier)", initFact.Value)
	}
	if initFact.Unit != fact.UnitRatio {
		t.Errorf("init_margin_rate unit = %q, want %q", initFact.Unit, fact.UnitRatio)
	}
	if initFact.Src != "mt5" || initFact.Venue != "v6" || initFact.Symbol != "EURUSD" {
		t.Errorf("init_margin_rate fact = %+v, want src=mt5 venue=v6 symbol=EURUSD", *initFact)
	}

	// mntn_margin_rate fact：首档 0.005。
	if mntnFact == nil {
		t.Fatal("mntn_margin_rate fact missing")
	}
	if mntnFact.Value != 0.005 {
		t.Errorf("mntn_margin_rate value = %v, want 0.005 (first tier)", mntnFact.Value)
	}
	if mntnFact.Unit != fact.UnitRatio {
		t.Errorf("mntn_margin_rate unit = %q, want %q", mntnFact.Unit, fact.UnitRatio)
	}
}

// TestSwapFacts_MarginRateMissingNotEmitted 缺字段（空切片）不发 fact。
func TestSwapFacts_MarginRateMissingNotEmitted(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v6", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			// margin rate 字段缺失（空切片 / nil）
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
	}}
	a := &MT5Adapter{venue: "v6", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD"})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	for _, f := range fs {
		if f.Kind == fact.KindInitMarginRate || f.Kind == fact.KindMntnMarginRate {
			t.Errorf("margin rate fact should not be emitted when field missing, got %+v", f)
		}
	}
}

// TestSwapFacts_MarginRatePartialOnlyInit 只有 InitMarginRate（Mntn 缺）→ 只发 init。
func TestSwapFacts_MarginRatePartialOnlyInit(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v6", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"EURUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			SwapType: mt5pb.SwapType_SwapType_InPoints, SwapLong: 1, SwapShort: -1, TradeMode: mt5pb.TradeMode_TradeMode_FullAccess,
			InitMarginRate: []float64{0.02},
			// MntnMarginRate 缺
		}, SymbolInfo: &mt5pb.SymbolInfo{Points: 0.00001, ContractSize: 100000}}},
	}}
	a := &MT5Adapter{venue: "v6", mt5: client}
	a.SetSymbolMap(map[string]string{"EURUSD": "EURUSD"})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	hasInit, hasMntn := false, false
	for _, f := range fs {
		if f.Kind == fact.KindInitMarginRate {
			hasInit = true
			if f.Value != 0.02 {
				t.Errorf("init_margin_rate value = %v, want 0.02", f.Value)
			}
		}
		if f.Kind == fact.KindMntnMarginRate {
			hasMntn = true
		}
	}
	if !hasInit {
		t.Error("init_margin_rate fact should be emitted (field present)")
	}
	if hasMntn {
		t.Error("mntn_margin_rate fact should NOT be emitted (field missing)")
	}
}

// TestSwapFacts_MarginRateEmittedForSkipSymbols margin rate 对标定 skip 的符号也发
// （跟随 tradeModeFact 先例：不依赖 swap 标定）。
func TestSwapFacts_MarginRateEmittedForSkipSymbols(t *testing.T) {
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "EURUSD", Venue: "v6", VenueType: "mt5", Bid: 1.1, Ask: 1.1001, Time: time.Now()})
	client := &fakeMT5Client{params: map[string]*mt5pb.SymbolParamsReply{
		"ETHUSD": {Result: &mt5pb.SymbolParams{SymbolGroup: &mt5pb.SymGroup{
			// Uncalibrated：None+非零矛盾 → swap 标定 skip
			SwapType: mt5pb.SwapType_SwapType_SwapNone, SwapLong: 0.1, TradeMode: mt5pb.TradeMode_TradeMode_LongOnly,
			InitMarginRate: []float64{0.03},
			MntnMarginRate: []float64{0.015},
		}}},
	}}
	a := &MT5Adapter{venue: "v6", mt5: client}
	a.SetSymbolMap(map[string]string{"ETHUSD": "ETHUSDT"})

	fs, err := a.SwapFacts(context.Background(), q)
	if err != nil {
		t.Fatalf("SwapFacts: %v", err)
	}
	hasInit, hasMntn := false, false
	for _, f := range fs {
		if f.Kind == fact.KindInitMarginRate && f.Symbol == "ETHUSDT" {
			hasInit = true
		}
		if f.Kind == fact.KindMntnMarginRate && f.Symbol == "ETHUSDT" {
			hasMntn = true
		}
	}
	if !hasInit || !hasMntn {
		t.Errorf("margin rate facts should be emitted for skip symbols (follows tradeModeFact precedent), got init=%v mntn=%v", hasInit, hasMntn)
	}
}

// TestFirstMarginRate 单元测试 firstMarginRate 辅助函数。
func TestFirstMarginRate(t *testing.T) {
	cases := []struct {
		name   string
		rates  []float64
		want   float64
		wantOk bool
	}{
		{"nil", nil, 0, false},
		{"empty", []float64{}, 0, false},
		{"single", []float64{0.01}, 0.01, true},
		{"multi_first", []float64{0.01, 0.02, 0.05}, 0.01, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := firstMarginRate(c.rates)
			if ok != c.wantOk {
				t.Fatalf("ok = %v, want %v", ok, c.wantOk)
			}
			if ok && got != c.want {
				t.Errorf("value = %v, want %v", got, c.want)
			}
		})
	}
}
