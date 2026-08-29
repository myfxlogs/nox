package adapter

// D-142 ③ 单测：品种级保证金率采集（MetaFacts 扩展）。
// 用例①：InitMarginRate 有值 → 产出 mt_symbol_margin fact
// 用例②：InitMarginRate 空（网关实拍现状）→ 无 margin fact，CS fact 正常产出
// 用例③：InitMarginRate[0]=0（无限杠杆品种）→ 跳过

import (
	"context"
	"testing"

	"arb/internal/fact"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// 用例①：InitMarginRate 有值 → 产出 mt_symbol_margin fact（value=InitMarginRate[0], unit=ratio）。
func TestMetaFacts_MarginRatePresent(t *testing.T) {
	client := &fakeMT5Client{
		paramsMany: &mt5pb.SymbolParamsManyReply{
			Result: []*mt5pb.SymbolParams{
				{
					Symbol:     "EURUSDm",
					SymbolInfo: &mt5pb.SymbolInfo{ContractSize: 100000},
					SymbolGroup: &mt5pb.SymGroup{
						InitMarginRate: []float64{0.01, 0.01, 0.01, 0.01}, // long/short/limit/stop
					},
				},
			},
		},
	}
	a := &MT5Adapter{venue: "vM", mt5: client}
	a.symMap.Store(&map[string]string{"EURUSDm": "EURUSD"})

	facts, err := a.MetaFacts(context.Background())
	if err != nil {
		t.Fatalf("MetaFacts: %v", err)
	}
	var metaCount, marginCount int
	for _, f := range facts {
		if f.Kind == fact.KindMTSymbolMeta {
			metaCount++
		}
		if f.Kind == fact.KindMTSymbolMargin {
			marginCount++
			if f.Value != 0.01 {
				t.Errorf("margin value: got %v, want 0.01", f.Value)
			}
			if f.Unit != fact.UnitRatio {
				t.Errorf("margin unit: got %s, want %s", f.Unit, fact.UnitRatio)
			}
			if f.Symbol != "EURUSDm" {
				t.Errorf("margin symbol: got %s, want EURUSDm", f.Symbol)
			}
		}
	}
	if metaCount != 1 {
		t.Errorf("meta facts: got %d, want 1", metaCount)
	}
	if marginCount != 1 {
		t.Errorf("margin facts: got %d, want 1", marginCount)
	}
}

// 用例②：InitMarginRate 空（网关实拍现状 2026-08-29）→ 无 margin fact，CS fact 正常产出。
func TestMetaFacts_MarginRateAbsent_GatewayReality(t *testing.T) {
	client := &fakeMT5Client{
		paramsMany: &mt5pb.SymbolParamsManyReply{
			Result: []*mt5pb.SymbolParams{
				{
					Symbol:     "XAUUSDm",
					SymbolInfo: &mt5pb.SymbolInfo{ContractSize: 100},
					SymbolGroup: &mt5pb.SymGroup{
						InitMarginRate: nil, // 网关实拍不返回
					},
				},
			},
		},
	}
	a := &MT5Adapter{venue: "vM", mt5: client}
	a.symMap.Store(&map[string]string{"XAUUSDm": "XAUUSD"})

	facts, err := a.MetaFacts(context.Background())
	if err != nil {
		t.Fatalf("MetaFacts: %v", err)
	}
	for _, f := range facts {
		if f.Kind == fact.KindMTSymbolMargin {
			t.Errorf("不应产出 margin fact（InitMarginRate 空），got %+v", f)
		}
	}
	// CS fact 仍应产出
	hasCS := false
	for _, f := range facts {
		if f.Kind == fact.KindMTSymbolMeta {
			hasCS = true
		}
	}
	if !hasCS {
		t.Error("CS fact 应正常产出（InitMarginRate 空 ≠ CS 不可用）")
	}
}

// 用例③：InitMarginRate[0]=0（无限杠杆品种）→ 跳过 margin fact。
func TestMetaFacts_MarginRateZero_Skipped(t *testing.T) {
	client := &fakeMT5Client{
		paramsMany: &mt5pb.SymbolParamsManyReply{
			Result: []*mt5pb.SymbolParams{
				{
					Symbol:     "AUDJPYm",
					SymbolInfo: &mt5pb.SymbolInfo{ContractSize: 100000},
					SymbolGroup: &mt5pb.SymGroup{
						InitMarginRate: []float64{0, 0, 0, 0}, // 无限杠杆 → 0
					},
				},
			},
		},
	}
	a := &MT5Adapter{venue: "vM", mt5: client}
	a.symMap.Store(&map[string]string{"AUDJPYm": "AUDJPY"})

	facts, err := a.MetaFacts(context.Background())
	if err != nil {
		t.Fatalf("MetaFacts: %v", err)
	}
	for _, f := range facts {
		if f.Kind == fact.KindMTSymbolMargin {
			t.Errorf("不应产出 margin fact（InitMarginRate[0]=0 无限杠杆），got %+v", f)
		}
	}
}
