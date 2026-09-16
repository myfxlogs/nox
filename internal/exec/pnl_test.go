package exec

import (
	"context"
	"math"
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

// permissiveCSResolver 任意 venueKey:symbol → CS=1（D-311 R4：存量测试管线接线用——
// :perp 腿执行器边界换算需要 cs，cs=1 时 orderQty=leg.Qty 与旧行为一致，原断言不受影响）。
type permissiveCSResolver struct{}

func (permissiveCSResolver) ContractSize(venueKey, venueSymbol string) (float64, bool) {
	return 1, true
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
	got := p.calcLegPnL(leg, fill, 1.0)
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
	got := p.calcLegPnL(leg, fill, 1.0)
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
	got := p.calcLegPnL(leg, fill, 1.0)
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
	got := p.calcLegPnL(leg, fill, 1.0)
	// D-159 W3a R4：双计拆除——calcLegPnL 只算价格差 PnL，swap/commission 走 cashflows。
	// priceDiff = 1.0×0.01×100×1 = $1.0（旧公式 +swap 0.5 -commission 0.1 = $1.4 双计）
	want := 1.0
	if got != want {
		t.Errorf("PnL with swap+commission want $%.2f (price diff only, swap/commission via cashflows), got $%.2f", want, got)
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
	got := p.calcLegPnL(leg, Fill{Price: 2001.0}, 1.0)
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
	got := p.calcLegPnL(leg, fill, 1.0)
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
	got, ok := p.calibratePnL(context.Background(), legs, 1.0)
	if !ok {
		t.Fatal("want ok=true (calibrated)")
	}
	// D-159 W3a R4：权威值只用价格 PnL（Profit），Swap/Commission 走 cashflows。
	// leg1: 10; leg2: -5; total=5.0（旧公式 10.4+(-4.75)=5.65 双计）
	want := 5.0
	if got != want {
		t.Errorf("authoritative PnL want %.2f (Profit only, swap/commission via cashflows), got %.2f", want, got)
	}
}

// TestCalibratePnL_NotFound 历史单未找到 → ok=false（用同步公式）。
func TestCalibratePnL_NotFound(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
	}
	cal := &fakePnLCalibrator{results: map[string]*HistoryPnL{}} // 空，查不到
	p := &Pipeline{pnlCalibrator: cal}
	_, ok := p.calibratePnL(context.Background(), legs, 1.0)
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
	_, ok := p.calibratePnL(context.Background(), legs, 1.0)
	if ok {
		t.Error("nil calibrator want ok=false")
	}
}

// TestCalibratePnL_PartialHit D-159 W3a R4：部分腿命中 → ok=false（部分成功不覆盖整仓，用同步公式）。
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
	_, ok := p.calibratePnL(context.Background(), legs, 1.0)
	if ok {
		t.Fatal("partial hit want ok=false (partial calibration does not override, R4)")
	}
}

// --- D-105 T2：计价币→USD 换算单测 ---

// TestCalcLegPnL_JPYFxConversion D-105 T2：JPY 计价腿 ×fx 换算 USD。
// AUDJPY BUY：open=113.5, fill=113.6, qty=1, CS=100000 → 价差 0.1×1×100000 = 10000 JPY；
// fx=1/150 ≈ 0.006667 → 10000 JPY ≈ $66.67（修复 #264 JPY 当 USD 的 -2700 量级错误）。
func TestCalcLegPnL_JPYFxConversion(t *testing.T) {
	openPrice := 113.5
	leg := pgstore.PositionLeg{
		Phase:       "OPEN",
		Status:      "FILLED",
		Venue:       "vA:mt5",
		Symbol:      "AUDJPY",
		Side:        "BUY",
		Qty:         1.0,
		PriceActual: &openPrice,
	}
	fill := Fill{Price: 113.6}
	cs := &fakeCSResolver{sizes: map[string]float64{"vA:mt5:AUDJPY": 100000.0}}
	p := &Pipeline{csResolver: cs}
	fx := 1.0 / 150.0
	got := p.calcLegPnL(leg, fill, fx)
	want := 10000.0 / 150.0 // ≈ 66.6667
	if math.Abs(got-want) > 1e-6 {
		t.Errorf("JPY ×fx want ~%.4f USD, got %.4f", want, got)
	}
}

// TestCalcLegPnL_FxMissingZero D-105 T2：fx=0（缺失）→ PnL=0（宁缺勿虚增，ClosePosition 发 alert）。
func TestCalcLegPnL_FxMissingZero(t *testing.T) {
	openPrice := 113.5
	leg := pgstore.PositionLeg{
		Phase:       "OPEN",
		Status:      "FILLED",
		Venue:       "vA:mt5",
		Symbol:      "AUDJPY",
		Side:        "BUY",
		Qty:         1.0,
		PriceActual: &openPrice,
	}
	fill := Fill{Price: 113.6, Swap: 5.0, Commission: 1.0}
	cs := &fakeCSResolver{sizes: map[string]float64{"vA:mt5:AUDJPY": 100000.0}}
	p := &Pipeline{csResolver: cs}
	got := p.calcLegPnL(leg, fill, 0) // fx=0 缺失
	if got != 0 {
		t.Errorf("fx missing want PnL=0 (宁缺勿虚增), got %.4f", got)
	}
}

// TestCalibratePnL_FxConversion D-105 T2：权威值 ×fx 换算 USD。
// D-159 W3a R4：权威值只用 Profit → 双腿 10+(-5)=5.0 × fx=0.5 → 2.5。
func TestCalibratePnL_FxConversion(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
		{Phase: "OPEN", Status: "FILLED", Venue: "vB:mt5", OrderID: "2002"},
	}
	cal := &fakePnLCalibrator{results: map[string]*HistoryPnL{
		"vA:mt5:1001": {Profit: 10.0, Swap: 0.5, Commission: 0.1},
		"vB:mt5:2002": {Profit: -5.0, Swap: 0.3, Commission: 0.05},
	}}
	p := &Pipeline{pnlCalibrator: cal}
	got, ok := p.calibratePnL(context.Background(), legs, 0.5)
	if !ok {
		t.Fatal("want ok=true (calibrated)")
	}
	want := 5.0 * 0.5 // 2.5（Profit only: 10+(-5)=5, ×0.5）
	if math.Abs(got-want) > 1e-9 {
		t.Errorf("calibrate ×fx want %.4f, got %.4f", want, got)
	}
}

// TestCalibratePnL_FxMissingZero D-105 T2：fx=0 → 权威值换算后 0（与 calcLegPnL 同口径 fail-closed）。
func TestCalibratePnL_FxMissingZero(t *testing.T) {
	legs := []pgstore.PositionLeg{
		{Phase: "OPEN", Status: "FILLED", Venue: "vA:mt5", OrderID: "1001"},
	}
	cal := &fakePnLCalibrator{results: map[string]*HistoryPnL{
		"vA:mt5:1001": {Profit: 10.0, Swap: 0, Commission: 0},
	}}
	p := &Pipeline{pnlCalibrator: cal}
	got, ok := p.calibratePnL(context.Background(), legs, 0)
	if !ok {
		t.Fatal("fx=0 still calibrated (ok=true), value should be 0")
	}
	if got != 0 {
		t.Errorf("fx=0 want 0, got %.4f", got)
	}
}

// TestCalcLegPnL_CexLegNoContractSize D-311 R5 缺陷#6a：持久化 leg.Qty 对 CEX 腿=基础币
// （Fill.Qty 契约），csResolver 的 ct_val 不得再乘——gate ct_val=0.01 qty=15.75 基础币
// → PnL=diff×15.75；okx ct_val=1000 qty=2950 基础币 → PnL=diff×2950（生产实证曾放千倍）。
func TestCalcLegPnL_CexLegNoContractSize(t *testing.T) {
	p := &Pipeline{csResolver: &fakeCSResolver{sizes: map[string]float64{
		"gate:perp:CL_USDT":      0.01,
		"okx:perp:TRX-USDT-SWAP": 1000,
	}}}

	open := 63.5
	gateLeg := pgstore.PositionLeg{
		Phase: "OPEN", Status: "FILLED", Venue: "gate:perp", Symbol: "CL_USDT",
		Side: "BUY", Qty: 15.75, PriceActual: &open,
	}
	got := p.calcLegPnL(gateLeg, Fill{Price: 64.5, Qty: 15.75}, 1.0)
	if want := 15.75; got != want {
		t.Errorf("gate:perp PnL want $%.2f（qty=基础币不乘 ct_val，×0.01 会得 $0.16）, got $%.2f", want, got)
	}

	open2 := 0.10
	okxLeg := pgstore.PositionLeg{
		Phase: "OPEN", Status: "FILLED", Venue: "okx:perp", Symbol: "TRX-USDT-SWAP",
		Side: "BUY", Qty: 2950, PriceActual: &open2,
	}
	got = p.calcLegPnL(okxLeg, Fill{Price: 0.11, Qty: 2950}, 1.0)
	if want := 29.5; math.Abs(got-want) > 1e-9 {
		t.Errorf("okx:perp PnL want $%.2f（×ct_val=1000 会得 $29500）, got $%.6f", want, got)
	}
}
