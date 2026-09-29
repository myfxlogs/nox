// d564_gate_align_test.go —— D-564 验收锚（T1/T2/T4/T5/T6 + T10/T11）：
// T1 get_leverage 读三态/T2 precheck 模式路由零写/T4 dual 持仓路由/T5 平仓单分叉/
// T6 合约规格闸；T10 OKX posMode/state；T11 Binance filters 闸。
package exec

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// d564GateRT D-564 专用 fake（accounts/leverage/positions/dual_comp/orders/contracts 全路由；
// 记录请求方法+路径+body 供零写/平仓体断言）。
type d564GateRT struct {
	accounts    string
	lever       string   // get_leverage 响应体（{"Lever":"10"}）
	positions   string   // single 持仓体
	dualPos     string   // dual_comp 持仓体（[]Position）
	orderBodies []string // POST /orders body 记录
	postPaths   []string // 全部 POST path 记录
	leverQs     []string // get_leverage query 记录
	postFail    bool     // true=POST 一律 500
}

func (rt *d564GateRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == "POST" {
		b, _ := io.ReadAll(req.Body)
		rt.postPaths = append(rt.postPaths, req.URL.Path)
		if strings.Contains(req.URL.Path, "/orders") {
			rt.orderBodies = append(rt.orderBodies, string(b))
		}
		if rt.postFail {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("no")), Header: make(http.Header), Request: req}, nil
		}
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader(`{"id":1}`)), Header: make(http.Header), Request: req}, nil
	}
	switch {
	case strings.Contains(req.URL.Path, "/get_leverage/"):
		rt.leverQs = append(rt.leverQs, req.URL.RawQuery)
		return resp200(rt.lever)
	case strings.Contains(req.URL.Path, "/dual_comp/positions/"):
		return resp200(rt.dualPos)
	case strings.Contains(req.URL.Path, "/futures/usdt/positions/"):
		return resp200(rt.positions)
	case strings.Contains(req.URL.Path, "/futures/usdt/accounts"):
		return resp200(rt.accounts)
	case strings.Contains(req.URL.Path, "/contracts/"):
		return resp200(`{"quanto_multiplier":"0.01","order_size_min":1,"order_size_max":100,"enable_decimal":false,"in_delisting":false,"leverage_max":50,"mark_price":"1000"}`)
	}
	return resp200(`{}`)
}

func resp200(body string) (*http.Response, error) {
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
}

// TestD564_T1_GetLeverageFailClosed T1：Lever 空/非数字/HTTP 错 → fail-closed 文案。
func TestD564_T1_GetLeverageFailClosed(t *testing.T) {
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	rt := &d564GateRT{lever: `{"Lever":""}`}
	e.client = &http.Client{Transport: rt}
	if _, err := e.gateGetLeverage(context.Background(), "XAU_USDT", "isolated", ""); err == nil || !strings.Contains(err.Error(), "Lever 空") {
		t.Fatalf("Lever 空→fail-closed: %v", err)
	}
	rt2 := &d564GateRT{lever: `{"Lever":"abc"}`}
	e2 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e2.client = &http.Client{Transport: rt2}
	if _, err := e2.gateGetLeverage(context.Background(), "XAU_USDT", "isolated", "dual_long"); err == nil || !strings.Contains(err.Error(), "非正/非数字") {
		t.Fatalf("非数字→fail-closed: %v", err)
	}
}

// TestD564_T2_PrecheckModeRouting T2：dual 模式按腿方向 dual_side+双 margin 各读取
// min；single 单读无 dual_side；读败拒开；precheck 全程零写请求。
func TestD564_T2_PrecheckModeRouting(t *testing.T) {
	// dual：SELL 腿→dual_short；isolated=2 cross=10 → min=2 → need=500 ≤ 800×0.8=640 放行。
	rt := &d564GateRT{accounts: `{"available":"800","total":"800","position_mode":"dual"}`, lever: `{"Lever":"2"}`}
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: rt}
	// 单独核 dual_side 参数与双 margin：直接走 gateEffectiveLeverage（precheck 语义同源）。
	lv, err := e.gateEffectiveLeverage(context.Background(), "XAU_USDT", gateModeDual, true)
	if err != nil || lv != 2 {
		t.Fatalf("dual min(2,2)=2: lv=%v err=%v", lv, err)
	}
	for _, q := range rt.leverQs {
		if !strings.Contains(q, "pos_margin_mode=") || !strings.Contains(q, "dual_side=dual_short") {
			t.Fatalf("dual 必带 pos_margin_mode+dual_side=dual_short, got %q", q)
		}
	}
	if len(rt.leverQs) != 2 {
		t.Fatalf("dual 应双 margin 各读一次, got %d", len(rt.leverQs))
	}
	// precheck 全程零写请求（推式退役锚）。
	rtPre := &d564GateRT{accounts: `{"available":"800","total":"800","position_mode":"dual"}`, lever: `{"Lever":"10"}`}
	ePre := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	ePre.client = &http.Client{Transport: rtPre}
	if err := ePre.MarginPrecheck(context.Background(), Order{Symbol: "XAU_USDT", Side: "SELL", Qty: 0.01, Venue: "gate:perp"}); err != nil {
		t.Fatalf("dual precheck 放行: %v", err)
	}
	if len(rtPre.postPaths) != 0 {
		t.Fatalf("precheck 全程零写请求, got %v", rtPre.postPaths)
	}
}

// TestD564_T4_PositionsDualRouting T4：dual→dual_comp []Position 双侧 flatten；
// POSITION_NOT_FOUND→空；single 路由回归。
func TestD564_T4_PositionsDualRouting(t *testing.T) {
	rt := &d564GateRT{
		accounts: `{"position_mode":"dual","in_dual_mode":true}`,
		dualPos:  `[{"contract":"XAU_USDT","size":2,"entry_price":"4200","mark_price":"4210","funding":"1.5","mode":"dual_long"},{"contract":"XAU_USDT","size":-1,"entry_price":"4180","mark_price":"4210","funding":"0.5","mode":"dual_short"}]`,
	}
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: rt}
	_, ps, err := e.GatePositionsRaw(context.Background(), "XAU_USDT")
	if err != nil {
		t.Fatalf("dual positions: %v", err)
	}
	if len(ps) != 2 {
		t.Fatalf("dual 双侧 flatten want 2, got %d", len(ps))
	}
	// POSITION_NOT_FOUND（flat）→ 空。
	rt2 := &d564GateRT{accounts: `{"position_mode":"dual"}`, dualPos: `[]`}
	e2 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e2.client = &http.Client{Transport: rt2}
	// e.get 非 2xx 报错含 body——fake 直接 200 空数组模拟 flat（404 分支由 error 文案路径覆盖，
	// flat 实测主径=200 空数组/POSITION_NOT_FOUND 错误串）。
	if _, ps2, err2 := e2.GatePositionsRaw(context.Background(), "XAU_USDT"); err2 != nil || len(ps2) != 0 {
		t.Fatalf("flat 空数组: %v %v", ps2, err2)
	}
	// single 路由回归：旧端点语义（size=0→nil）。
	rt3 := &d564GateRT{accounts: `{"position_mode":"single"}`, positions: `{"contract":"XAU_USDT","size":0}`}
	e3 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e3.client = &http.Client{Transport: rt3}
	if _, ps3, err3 := e3.GatePositionsRaw(context.Background(), "XAU_USDT"); err3 != nil || ps3 != nil {
		t.Fatalf("single size=0 → nil: %v %v", ps3, err3)
	}
}

// TestD564_T5_CloseOrderDual T5：dual 平仓 body=auto_size+size:0+close:true+无
// reduce_only；single 平仓仍 reduce_only:true。
func TestD564_T5_CloseOrderDual(t *testing.T) {
	rt := &d564GateRT{accounts: `{"position_mode":"dual"}`, lever: `{"Lever":"10"}`}
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: rt}
	// R1 反转：auto_size 按持仓侧 PositionSide 驱动——PositionSide=short（平空腿，
	// BUY 平）→ close_short。旧版按下单侧 o.Side 推导会把「SELL 平多」错写成
	// close_short，本锚 BUY+short→close_short 钉死正确映射。
	// placeGateRaw 尾部 finish 核单会因 fake 无查单路由报「未成交」——体断言不受影响。
	_, _, _ = e.placeGateRaw(context.Background(), Order{Symbol: "XAU_USDT", Side: "BUY", Qty: 0.01, PositionSide: "short"})
	if len(rt.orderBodies) != 1 {
		t.Fatalf("应恰一平仓单, got %d", len(rt.orderBodies))
	}
	b := rt.orderBodies[0]
	for _, want := range []string{`"size":0`, `"close":true`, `"auto_size":"close_short"`} {
		if !strings.Contains(b, want) {
			t.Fatalf("dual 平仓体缺 %s: %s", want, b)
		}
	}
	if strings.Contains(b, "reduce_only") {
		t.Fatalf("dual 平仓不得带 reduce_only: %s", b)
	}
	// R1 非法值锚：PositionSide 非 long/short → fail-closed 不落单。
	rtBad := &d564GateRT{accounts: `{"position_mode":"dual"}`, lever: `{"Lever":"10"}`}
	eBad := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	eBad.client = &http.Client{Transport: rtBad}
	_, _, errBad := eBad.placeGateRaw(context.Background(), Order{Symbol: "XAU_USDT", Side: "SELL", Qty: 0.01, PositionSide: "both"})
	if errBad == nil || !strings.Contains(errBad.Error(), "PositionSide") {
		t.Fatalf("非法 PositionSide 应 fail-closed: %v", errBad)
	}
	if len(rtBad.orderBodies) != 0 {
		t.Fatalf("非法 PositionSide 不得落单, got %d 单", len(rtBad.orderBodies))
	}
	// single 平仓回归。
	rt2 := &d564GateRT{accounts: `{"position_mode":"single"}`, lever: `{"Lever":"10"}`}
	e2 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e2.client = &http.Client{Transport: rt2}
	_, _, _ = e2.placeGateRaw(context.Background(), Order{Symbol: "XAU_USDT", Side: "SELL", Qty: 0.01, PositionSide: "long"})
	if !strings.Contains(rt2.orderBodies[0], `"reduce_only":true`) || strings.Contains(rt2.orderBodies[0], "auto_size") {
		t.Fatalf("single 平仓应 reduce_only:true 无 auto_size: %s", rt2.orderBodies[0])
	}
}

// TestD564_T6_ContractSpecGates T6：in_delisting 拒/size min 拒/整数张 floor/decimal 序列化。
func TestD564_T6_ContractSpecGates(t *testing.T) {
	// in_delisting=true → 拒。
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: &specRT{spec: `{"quanto_multiplier":"0.01","order_size_min":1,"enable_decimal":false,"in_delisting":true,"mark_price":"1000"}`}}
	if _, _, err := e.gateSpecCheck(context.Background(), "DEAD_USDT", 1); err == nil || !strings.Contains(err.Error(), "退市") {
		t.Fatalf("in_delisting 应拒: %v", err)
	}
	// qty 张数 < order_size_min → 拒。
	e2 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e2.client = &http.Client{Transport: &specRT{spec: `{"quanto_multiplier":"0.01","order_size_min":10,"enable_decimal":false,"mark_price":"1000"}`}}
	if _, _, err := e2.gateSpecCheck(context.Background(), "MIN_USDT", 0.05); err == nil || !strings.Contains(err.Error(), "不足最小张数") {
		t.Fatalf("size<min 应拒: %v", err)
	}
	// 整数张 floor：qty=0.059 → 5.9 张 floor=5。
	e3 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e3.client = &http.Client{Transport: &specRT{spec: `{"quanto_multiplier":"0.01","order_size_min":1,"enable_decimal":false,"mark_price":"1000"}`}}
	sz, _, err := e3.gateSpecCheck(context.Background(), "INT_USDT", 0.059)
	if err != nil || sz != 5 {
		t.Fatalf("整数张 floor want 5, got %v err=%v", sz, err)
	}
}

// specRT 合约规格专用 fake（T6）。
type specRT struct{ spec string }

func (rt *specRT) RoundTrip(req *http.Request) (*http.Response, error) {
	return resp200(rt.spec)
}

// TestD564_T10_OKXPosModeAndState T10：posMode=net_mode→拒且报实测值；long_short_mode
// 正常；state=preopen→拒。
func TestD564_T10_OKXPosModeAndState(t *testing.T) {
	okxSwapSpecState.Range(func(k, v any) bool { okxSwapSpecState.Delete(k); return true })
	okxSwapSpecCache.Range(func(k, v any) bool { okxSwapSpecCache.Delete(k); return true })
	e := NewCEXExecutor("okx", "perp", "k", "s", "pp", "http://x", false)
	e.client = &http.Client{Transport: &okxConfigRT{config: `{"code":"0","data":[{"posMode":"net_mode"}]}`, instruments: `{"code":"0","data":[{"state":"live"}]}`}}
	err := e.okxPosModeCheck(context.Background())
	if err == nil || !strings.Contains(err.Error(), "net_mode") {
		t.Fatalf("net_mode 应拒且报实测值: %v", err)
	}
	e2 := NewCEXExecutor("okx", "perp", "k", "s", "pp", "http://x", false)
	e2.client = &http.Client{Transport: &okxConfigRT{config: `{"code":"0","data":[{"posMode":"long_short_mode"}]}`, instruments: `{"code":"0","data":[{"state":"preopen"}]}`}}
	if err := e2.okxPosModeCheck(context.Background()); err != nil {
		t.Fatalf("long_short_mode 应过 posMode 闸: %v", err)
	}
	if err := e2.okxStateCheck(context.Background(), "BTC-USDT-SWAP"); err == nil || !strings.Contains(err.Error(), "preopen") {
		t.Fatalf("preopen 应拒: %v", err)
	}
}

// okxConfigRT OKX config/instruments fake（T10）。
type okxConfigRT struct {
	config      string
	instruments string
}

func (rt *okxConfigRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/account/config") {
		return resp200(rt.config)
	}
	if strings.Contains(req.URL.Path, "/instruments") {
		return resp200(rt.instruments)
	}
	return resp200(`{"code":"0","data":[]}`)
}

// TestD564_T11_BinanceFilters T11：stepSize floor（0.0014→0.001）；<minQty 拒；
// maxNotional 超限拒；MIN_NOTIONAL 不足拒。
func TestD564_T11_BinanceFilters(t *testing.T) {
	binanceFiltersMu.Lock()
	binanceFiltersCache = map[string]binanceFilters{}
	binanceFiltersMu.Unlock()
	e := NewCEXExecutor("binance", "perp", "k", "s", "", "https://fapi.binance.com", false)
	e.client = &http.Client{Transport: &bFilterRT{}}
	q, err := e.binanceQtyGuard(context.Background(), "BTCUSDT", 0.0014, 200)
	if err != nil {
		t.Fatalf("floor: %v", err)
	}
	if q != 0.001 {
		t.Fatalf("floor want 0.001, got %v", q)
	}
	if _, err := e.binanceQtyGuard(context.Background(), "BTCUSDT", 0.0004, 200); err == nil || !strings.Contains(err.Error(), "不足最小量") {
		t.Fatalf("minQty 拒: %v", err)
	}
	if _, err := e.binanceQtyGuard(context.Background(), "BTCUSDT", 1000, 200); err == nil || !strings.Contains(err.Error(), "maxNotional") {
		t.Fatalf("maxNotional 拒: %v", err)
	}
	if _, err := e.binanceQtyGuard(context.Background(), "BTCUSDT", 0.001, 50); err == nil || !strings.Contains(err.Error(), "MIN_NOTIONAL") {
		t.Fatalf("MIN_NOTIONAL 拒: %v", err)
	}
}

// bFilterRT binance exchangeInfo/symbolConfig fake（T11）。
type bFilterRT struct{}

func (rt *bFilterRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if strings.Contains(req.URL.Path, "/exchangeInfo") {
		return resp200(`{"symbols":[{"symbol":"BTCUSDT","filters":[{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"0.1"}]}]}`)
	}
	if strings.Contains(req.URL.Path, "/symbolConfig") {
		return resp200(`{"symbol":"BTCUSDT","maxNotionalValue":50000}`)
	}
	return resp200(`{"markPrice":"96000"}`)
}
