package exec

import (
	"context"
	"testing"

	"arb/internal/store/pgstore"
)

// fakeCSResolver 测试用 ContractSizeResolver（D-077 修复⑪A M1）。
type fakeCSResolver struct {
	sizes map[string]float64 // key = "venueKey:symbol"
}

func (f *fakeCSResolver) ContractSize(venueKey, venueSymbol string) (float64, bool) {
	v, ok := f.sizes[venueKey+":"+venueSymbol]
	return v, ok
}

// fakePnLCalibrator 测试用 PnLCalibrator（D-077 修复⑪A M1 回读校准）。
type fakePnLCalibrator struct {
	results map[string]*HistoryPnL // key = "venueKey:ticket"
	err     error
}

func (f *fakePnLCalibrator) QueryHistoryPnL(ctx context.Context, venueKey, ticket string, fromDays int) (*HistoryPnL, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.results[venueKey+":"+ticket], nil
}

// TestCalcLegPnL_XAUUSD_100xRegression XAUUSD 量级回归锚（旧公式差 100 倍）。
// XAUUSD: CS=100（盎司/手），qty=0.01 手，价差 $1.0 → PnL = 1.0×0.01×100×1 = $1.0
// 旧公式（漏 CS）：1.0×0.01×1 = $0.01（差 100 倍）
func TestCalcLegPnL_XAUUSD_100xRegression(t *testing.T) {
	openPrice := 2000.0
	leg := pgstore.PositionLeg{
		Phase:       "OPEN",
		Status:      "FILLED",
		Venue:       "vA:mt5",
		Symbol:      "XAUUSDm",
		Side:        "BUY",
		Qty:         0.01,
		PriceActual: &openPrice,
	}
	fill := Fill{Price: 2001.0, Qty: 0.01} // 价差 $1.0
	cs := &fakeCSResolver{sizes: map[string]float64{"vA:mt5:XAUUSDm": 100.0}}
	p := &Pipeline{csResolver: cs}
	got := p.calcLegPnL(leg, fill)
	want := 1.0 // (2001-2000) × 0.01 × 100 × 1 = $1.0
	if got != want {
		t.Errorf("XAUUSD PnL want $%.2f (with CS=100), got $%.2f (old formula would give $0.01)", want, got)
	}
}

// TestCalcLegPnL_SELLLegDirection SELL 腿方向 = -1。
func TestCalcLegPnL_SELLLegDirection(t *testing.T) {
	openPrice := 2001.0
	leg := pgstore.PositionLeg{
		Venue:       "vA:mt5",
		Symbol:      "XAUUSDm",
		Side:        "SELL",
		Qty:         0.01,
		PriceActual: &openPrice,
	}
	fill := Fill{Price: 2000.0} // 价格跌 $1 → SELL 盈利
	cs := &fakeCSResolver{sizes: map[string]float64{"vA:mt5:XAUUSDm": 100.0}}
	p := &Pipeline{csResolver: cs}
	got := p.calcLegPnL(leg, fill)
	want := 1.0 // (2000-2001) × 0.01 × 100 × (-1) = $1.0
	if got != want {
		t.Errorf("SELL leg PnL want $%.2f, got $%.2f", want, got)
	}
}

// TestCalcLegPnL_CEX_CS1 CEX 腿 CS=1（qty 即基础币数）。
func TestCalcLegPnL_CEX_CS1(t *testing.T) {
	openPrice := 50000.0
	leg := pgstore.PositionLeg{
		Venue:       "binance:spot",
		Symbol:      "BTCUSDT",
		Side:        "BUY",
		Qty:         0.001,
		PriceActual: &openPrice,
	}
	fill := Fill{Price: 50100.0} // 价差 $100
	p := &Pipeline{}             // csResolver=nil → CS 回退 1.0
	got := p.calcLegPnL(leg, fill)
	want := 0.1 // (50100-50000) × 0.001 × 1 × 1 = $0.1
	if got != want {
		t.Errorf("CEX PnL want $%.4f (CS=1), got $%.4f", want, got)
	}
}

// TestCalcLegPnL_SwapCommission PnL 含 swap 和 commission。
func TestCalcLegPnL_SwapCommission(t *testing.T) {
	openPrice := 2000.0
	leg := pgstore.PositionLeg{
		Venue:       "vA:mt5",
		Symbol:      "XAUUSDm",
		Side:        "BUY",
		Qty:         0.01,
		PriceActual: &openPrice,
	}
	fill := Fill{Price: 2001.0, Swap: 0.5, Commission: 0.1}
	cs := &fakeCSResolver{sizes: map[string]float64{"vA:mt5:XAUUSDm": 100.0}}
	p := &Pipeline{csResolver: cs}
	got := p.calcLegPnL(leg, fill)
	// priceDiff = 1.0×0.01×100×1 = $1.0; +swap 0.5 -commission 0.1 = $1.4
	want := 1.4
	if got != want {
		t.Errorf("PnL with swap+commission want $%.2f, got $%.2f", want, got)
	}
}

// TestCalcLegPnL_NilOpenPrice 开仓价缺失 → PnL=0（不猜）。
func TestCalcLegPnL_NilOpenPrice(t *testing.T) {
	leg := pgstore.PositionLeg{
		Venue:       "vA:mt5",
		Symbol:      "XAUUSDm",
		Side:        "BUY",
		Qty:         0.01,
		PriceActual: nil,
	}
	p := &Pipeline{}
	got := p.calcLegPnL(leg, Fill{Price: 2001.0})
	if got != 0 {
		t.Errorf("nil open price want PnL=0, got %.2f", got)
	}
}

// TestCalcLegPnL_CSNotFound CS 未知 → 回退 1.0（CEX 正确，MT 有量级误差）。
func TestCalcLegPnL_CSNotFound(t *testing.T) {
	openPrice := 2000.0
	leg := pgstore.PositionLeg{
		Venue:       "vA:mt5",
		Symbol:      "XAUUSDm",
		Side:        "BUY",
		Qty:         0.01,
		PriceActual: &openPrice,
	}
	fill := Fill{Price: 2001.0}
	cs := &fakeCSResolver{sizes: map[string]float64{}} // 空表，查不到
	p := &Pipeline{csResolver: cs}
	got := p.calcLegPnL(leg, fill)
	want := 0.01 // (2001-2000) × 0.01 × 1 × 1 = $0.01（CS 回退 1.0）
	if got != want {
		t.Errorf("CS not found fallback want $%.4f, got $%.4f", want, got)
	}
}

// TestCalibratePnL_Authoritative 历史单权威 PnL 覆盖同步公式。
func TestCalibratePnL_Authoritative(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
		{Phase: "OPEN", Status: "FILLED", Venue: "vB:mt5", OrderID: "2002"},
	}
	cal := &fakePnLCalibrator{results: map[string]*HistoryPnL{
		"vA:mt5:1001": {Profit: 10.0, Swap: 0.5, Commission: 0.1},
		"vB:mt5:2002": {Profit: -5.0, Swap: 0.3, Commission: 0.05},
	}}
	p := &Pipeline{pnlCalibrator: cal}
	got, ok := p.calibratePnL(context.Background(), legs)
	if !ok {
		t.Fatal("want ok=true (calibrated)")
	}
	// leg1: 10+0.5-0.1=10.4; leg2: -5+0.3-0.05=-4.75; total=5.65
	want := 5.65
	if got != want {
		t.Errorf("authoritative PnL want %.2f, got %.2f", want, got)
	}
}

// TestCalibratePnL_NotFound 历史单未找到 → ok=false（用同步公式）。
func TestCalibratePnL_NotFound(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
	}
	cal := &fakePnLCalibrator{results: map[string]*HistoryPnL{}} // 空，查不到
	p := &Pipeline{pnlCalibrator: cal}
	_, ok := p.calibratePnL(context.Background(), legs)
	if ok {
		t.Error("not found want ok=false")
	}
}

// TestCalibratePnL_NilCalibrator calibrator 未接线 → ok=false。
func TestCalibratePnL_NilCalibrator(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
	}
	p := &Pipeline{} // pnlCalibrator=nil
	_, ok := p.calibratePnL(context.Background(), legs)
	if ok {
		t.Error("nil calibrator want ok=false")
	}
}

// TestCalibratePnL_PartialHit 部分腿命中 → 用命中的腿（calibrated>0 即 ok）。
func TestCalibratePnL_PartialHit(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
		{Phase: "OPEN", Status: "FILLED", Venue: "vB:mt5", OrderID: "2002"},
	}
	cal := &fakePnLCalibrator{results: map[string]*HistoryPnL{
		"vA:mt5:1001": {Profit: 10.0, Swap: 0, Commission: 0},
		// vB:2002 未找到
	}}
	p := &Pipeline{pnlCalibrator: cal}
	got, ok := p.calibratePnL(context.Background(), legs)
	if !ok {
		t.Fatal("partial hit want ok=true")
	}
	if got != 10.0 {
		t.Errorf("partial hit PnL want 10.0 (only leg1), got %.2f", got)
	}
}
