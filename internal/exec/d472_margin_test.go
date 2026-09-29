// d472_margin_test.go —— D-472 S1/S2/S4：binance/gate MarginPrecheck 补实矩阵 +
// 强平距离闸分流（先红后绿锚：量纲钉死 base×mark/lev，禁二次乘 ct_val/quanto）。
package exec

import (
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"
)

// d472BinRT binance perp fake RoundTripper（/v1/positionSide/dual、/v2/account、
// /v1/premiumIndex、/v1/leverageBracket；baseURL 已含 /fapi 段同 placeBinance 惯例）。
type d472BinRT struct {
	dual      string // dualSidePosition 值："true"/"false"/""（=探测 500）
	account   string // /v2/account 原文；""=500
	markPrice string
	brackets  string
}

func (rt *d472BinRT) RoundTrip(req *http.Request) (*http.Response, error) {
	var body string
	switch {
	case strings.Contains(req.URL.Path, "/v1/positionSide/dual"):
		if rt.dual == "" {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("oops")), Header: make(http.Header), Request: req}, nil
		}
		body = `{"dualSidePosition":` + rt.dual + `}`
	case strings.Contains(req.URL.Path, "/v2/account"):
		if rt.account == "" {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("oops")), Header: make(http.Header), Request: req}, nil
		}
		body = rt.account
	case strings.Contains(req.URL.Path, "/v1/premiumIndex"):
		body = `{"markPrice":"` + rt.markPrice + `"}`
	case strings.Contains(req.URL.Path, "/v1/leverageBracket"):
		body = rt.brackets
	default:
		body = `{}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

// d472BinAcc /v2/account 形状（one-way BOTH 单行）。
func d472BinAcc(avail, equity, lev, isolated string) string {
	return `{"availableBalance":"` + avail + `","totalMarginBalance":"` + equity + `","positions":[
		{"symbol":"XPTUSDT","positionSide":"BOTH","leverage":"` + lev + `","isolated":` + isolated + `}]}`
}

// d472BinAccHedge hedge 双行（LONG/SHORT 杠杆可不同，B-002 锚）。
func d472BinAccHedge(avail, levLong, levShort, isolated string) string {
	return `{"availableBalance":"` + avail + `","totalMarginBalance":"1000","positions":[
		{"symbol":"XPTUSDT","positionSide":"LONG","leverage":"` + levLong + `","isolated":false},
		{"symbol":"XPTUSDT","positionSide":"SHORT","leverage":"` + levShort + `","isolated":` + isolated + `}]}`
}

// TestD472_BinanceMarginOneWay one-way：量纲锚（B-001）——o.Qty=base，required=qty×mark/lev
// =2×100/10=20；若实现二次乘 ct_val 或误用张数，need 值漂移即断言红。
func TestD472_BinanceMarginOneWay(t *testing.T) {
	e := NewCEXExecutor("binance", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: &d472BinRT{dual: "false", account: d472BinAcc("1000", "1000", "10", "false"), markPrice: "100"}}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "BUY", Qty: 2, Venue: "binance:perp"}); err != nil {
		t.Fatalf("required=20 ≤ 1000×0.8 应放行: %v", err)
	}
	e2 := NewCEXExecutor("binance", "perp", "k", "s", "", "http://x", false)
	e2.client = &http.Client{Transport: &d472BinRT{dual: "false", account: d472BinAcc("10", "10", "10", "false"), markPrice: "100"}}
	err := e2.MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "BUY", Qty: 2, Venue: "binance:perp"})
	if err == nil || !strings.Contains(err.Error(), "binance margin insufficient") {
		t.Fatalf("required=20 > 10×0.8=8 应拒: %v", err)
	}
	if !strings.Contains(err.Error(), "need 20.00") {
		t.Fatalf("量纲锚：need 应=2×100/10=20.00（禁二次乘 ct_val），got %v", err)
	}
}

// TestD472_BinanceHedgeSideMatch B-002：hedge 双行按 positionSide 匹配——SELL 腿用 SHORT
// 行杠杆 20（need=2×100/20=10≤15×0.8=12 放行）；若误用 LONG 行（lev=5，need=40>12）即拒=红。
func TestD472_BinanceHedgeSideMatch(t *testing.T) {
	e := NewCEXExecutor("binance", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: &d472BinRT{dual: "true", account: d472BinAccHedge("15", "5", "20", "false"), markPrice: "100"}}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "SELL", Qty: 2, Venue: "binance:perp"}); err != nil {
		t.Fatalf("hedge SELL 应匹配 SHORT 行（lev=20）放行: %v", err)
	}
}

// TestD472_BinanceHedgeNoSideRow B-002：hedge 模式同侧无行 → error 不猜。
func TestD472_BinanceHedgeNoSideRow(t *testing.T) {
	e := NewCEXExecutor("binance", "perp", "k", "s", "", "http://x", false)
	// 只有 BOTH 行的假响应在 hedge 探测下应不匹配（wantSide=SHORT ≠ BOTH）
	rt := &d472BinRT{dual: "true", account: d472BinAcc("1000", "1000", "10", "false"), markPrice: "100"}
	e.client = &http.Client{Transport: rt}
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "SELL", Qty: 2, Venue: "binance:perp"})
	if err == nil || !strings.Contains(err.Error(), "不猜") {
		t.Fatalf("hedge 同侧无行应 error 不猜，got %v", err)
	}
}

// TestD472_BinanceAPIFailClosed S1：/v2/account 5xx → fail-closed。
func TestD472_BinanceAPIFailClosed(t *testing.T) {
	e := NewCEXExecutor("binance", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: &d472BinRT{dual: "false", account: "", markPrice: "100"}}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "BUY", Qty: 2, Venue: "binance:perp"}); err == nil {
		t.Fatal("account 5xx 应 fail-closed 拒开")
	}
}

// TestD472_BinanceLiqGuardIsolated S4：isolated 分支——est=1/lev−mmr ≥ floor；
// MMR 运行时取 leverageBracket（fake 0.05）。
func TestD472_BinanceLiqGuardIsolated(t *testing.T) {
	br := `[{"symbol":"XPTUSDT","brackets":[{"bracket":1,"mmr":"0.05","cap":"50000"}]}]`
	mk := func(minPct float64) *CEXExecutor {
		e := NewCEXExecutor("binance", "perp", "k", "s", "", "http://x", false)
		e.client = &http.Client{Transport: &d472BinRT{dual: "false", account: d472BinAcc("1000", "1000", "10", "true"), markPrice: "100", brackets: br}}
		e.SetLiqGuards(minPct, 0.3)
		return e
	}
	if err := mk(0.05).MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "BUY", Qty: 2, Venue: "binance:perp"}); err != nil {
		t.Fatalf("est=1/10−0.05=0.05 ≥ floor=0.05 应放行: %v", err)
	}
	err := mk(0.06).MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "BUY", Qty: 2, Venue: "binance:perp"})
	if err == nil || !strings.Contains(err.Error(), "liq-distance est=0.0500 < floor=0.0600") {
		t.Fatalf("est=0.05 < floor=0.06 应拒（含 est 样式拒文）: %v", err)
	}
}

// TestD472_BinanceLiqGuardCrossCap S4：cross 分支——notional/equity ≤ cap（禁腿杠杆估）。
func TestD472_BinanceLiqGuardCrossCap(t *testing.T) {
	mk := func(equity, cap float64) *CEXExecutor {
		e := NewCEXExecutor("binance", "perp", "k", "s", "", "http://x", false)
		e.client = &http.Client{Transport: &d472BinRT{dual: "false", account: d472BinAcc("1000", ftoa(equity), "10", "false"), markPrice: "100"}}
		e.SetLiqGuards(0.05, cap)
		return e
	}
	if err := mk(1000, 0.3).MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "BUY", Qty: 2, Venue: "binance:perp"}); err != nil {
		t.Fatalf("ratio=200/1000=0.2 ≤ cap=0.3 应放行: %v", err)
	}
	err := mk(500, 0.3).MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "BUY", Qty: 2, Venue: "binance:perp"})
	if err == nil || !strings.Contains(err.Error(), "liq-distance cross notional/equity=0.400 > cap=0.300") {
		t.Fatalf("ratio=0.4 > cap=0.3 应拒: %v", err)
	}
}

// TestD472_LiqGuardDefaultOff S4：缺省 ARB_MIN_LIQ_DISTANCE_PCT=0=关闸（零额外语义）。
func TestD472_LiqGuardDefaultOff(t *testing.T) {
	e := NewCEXExecutor("binance", "perp", "k", "s", "", "http://x", false)
	// account: equity=1（名义 200 → ratio=200 巨额）——闸关=不因占比拒
	e.client = &http.Client{Transport: &d472BinRT{dual: "false", account: d472BinAcc("1000", "1", "10", "false"), markPrice: "100", brackets: `[{"symbol":"XPTUSDT","brackets":[{"mmr":"0.99"}]}]`}}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XPTUSDT", Side: "BUY", Qty: 2, Venue: "binance:perp"}); err != nil {
		t.Fatalf("闸缺省关（minPct=0）应放行: %v", err)
	}
}

func ftoa(f float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(strconv.FormatFloat(f, 'f', 2, 64)), "0"), ".")
}

// d472GateRT gate fake（/futures/usdt/accounts + /futures/usdt/contracts/{c}）。
type d472GateRT struct {
	accounts     string // ""=500
	mark         string
	posts        []string          // 收到的 POST path 记录（预检零 POST 断言用）
	leverBroken  bool              // D-564：get_leverage 返回 Lever 空（实况不可读 fail-closed 锚）
	levers       map[string]string // D-564：pos_margin_mode→Lever（dual 双 margin 取 min 锚）
	leverQueries []string          // D-564：get_leverage query 记录（dual_side 参数断言）
}

func (rt *d472GateRT) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.Method == "POST" {
		rt.posts = append(rt.posts, req.URL.Path)
		return &http.Response{StatusCode: 201, Body: io.NopCloser(strings.NewReader("{}")), Header: make(http.Header), Request: req}, nil
	}
	// D-564 S1：get_leverage 实况路由（leverage=10——原 setter 语义值移到 venue 实况）。
	if strings.Contains(req.URL.Path, "/get_leverage/") {
		rt.leverQueries = append(rt.leverQueries, req.URL.RawQuery)
		lever := `{"Lever":"10"}`
		if rt.leverBroken {
			lever = `{"Lever":""}`
		} else if rt.levers != nil {
			mode := "isolated"
			if strings.Contains(req.URL.RawQuery, "pos_margin_mode=cross") {
				mode = "cross"
			}
			lever = `{"Lever":"` + rt.levers[mode] + `"}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(lever)), Header: make(http.Header), Request: req}, nil
	}
	var body string
	switch {
	case strings.Contains(req.URL.Path, "/futures/usdt/accounts"):
		if rt.accounts == "" {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("oops")), Header: make(http.Header), Request: req}, nil
		}
		body = rt.accounts
	case strings.Contains(req.URL.Path, "/futures/usdt/contracts/"):
		body = `{"mark_price":"` + rt.mark + `"}`
	default:
		body = `{}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

// TestD472_GateMargin 目标杠杆注入档（D-563 S1：lev=venue 级配置唯一源，
// accounts.leverage 字段不再读、无兜底缺省——未配置 fail-closed）+量纲锚（quanto 不二次乘）。
// XPT_USDT quanto=0.001 实证：qty=1.0 base、mark=1000、lev=10 → need=100.00；
// 若实现 qty×quanto×mark=0.1 即量纲错 0.001 倍，need 断言抓住。
func TestD472_GateMargin(t *testing.T) {
	// lev=10 → need=1×1000/10=100 ≤ 500×0.8=400 放行
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: &d472GateRT{accounts: `{"available":"500","total":"500"}`, mark: "1000"}}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XPT_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"}); err != nil {
		t.Fatalf("lev=10 need=100 ≤ 400 应放行: %v", err)
	}
	// 量纲+拒：available=50 → need=100 > 40 拒，need 断言=100.00（非 0.1）
	e2 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e2.client = &http.Client{Transport: &d472GateRT{accounts: `{"available":"50","total":"50"}`, mark: "1000"}}
	err := e2.MarginPrecheck(context.Background(), Order{Symbol: "XPT_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"})
	if err == nil || !strings.Contains(err.Error(), "gate margin insufficient") || !strings.Contains(err.Error(), "need 100.00") {
		t.Fatalf("量纲锚 need=100.00（quanto 禁二次乘），got %v", err)
	}
	// D-564 纯读语义：杠杆=venue 实况（fake 恒 10）；accounts 无 leverage 字段不再有任何
	// 兜底链——实况可读即判（need=100 ≤ 250×0.8=200 放行）。
	e3 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e3.client = &http.Client{Transport: &d472GateRT{accounts: `{"available":"250","total":"250"}`, mark: "1000"}}
	if err := e3.MarginPrecheck(context.Background(), Order{Symbol: "XPT_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"}); err != nil {
		t.Fatalf("实况 lev=10（need=100 ≤ 200）放行: %v", err)
	}
	// get_leverage 实况不可读 → fail-closed 拒开（lev 实况上界=保守）。
	e7 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e7.client = &http.Client{Transport: &d472GateRT{accounts: `{"available":"250","total":"250"}`, mark: "1000", leverBroken: true}}
	if err := e7.MarginPrecheck(context.Background(), Order{Symbol: "XPT_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"}); err == nil ||
		!strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("杠杆实况不可读应 fail-closed, got %v", err)
	}
	// API err → fail-closed
	e6 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e6.client = &http.Client{Transport: &d472GateRT{accounts: "", mark: "1000"}}
	if err := e6.MarginPrecheck(context.Background(), Order{Symbol: "XPT_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"}); err == nil {
		t.Fatal("accounts 5xx 应 fail-closed 拒开")
	}
}

// TestD472_GateCrossLiqGuard S4：gate 恒 cross → 占比闸（notional/equity）。
func TestD472_GateCrossLiqGuard(t *testing.T) {
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: &d472GateRT{accounts: `{"available":"500","total":"100"}`, mark: "1000"}}
	e.SetLiqGuards(0.05, 0.3)
	// qty=1 base、mark=1000 → notional=1000；equity=100 → ratio=10 > 0.3 拒
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "XPT_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"})
	if err == nil || !strings.Contains(err.Error(), "gate liq-distance cross notional/equity=10.000 > cap=0.300") {
		t.Fatalf("cross 占比闸应拒: %v", err)
	}
}

// TestD564_GatePrecheckPureRead 三禁令①锚（D-564 取代 D-563 pushNotRead——纯读模型）：
// precheck=零写请求+get_leverage 实况消费（dual 双 margin 取 min）；
// 实况不可读=fail-closed 拒开（禁兜底禁配置冒充——配置链已退役）。
func TestD564_GatePrecheckPureRead(t *testing.T) {
	// ① dual 模式：双 margin（isolated+cross）各读一次取 min=2 → need=500（保守上界）；
	// precheck 全程零 POST。
	rt := &d472GateRT{accounts: `{"available":"800","total":"800","position_mode":"dual"}`, mark: "1000", levers: map[string]string{"isolated": "2", "cross": "10"}}
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: rt}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "XPT_USDT", Side: "SELL", Qty: 1, Venue: "gate:perp"}); err != nil {
		t.Fatalf("dual min(2,10)=2 → need=500 ≤ 800×0.8=640 放行, got %v", err)
	}
	if len(rt.posts) != 0 {
		t.Fatalf("precheck 全程零写请求（推式已退役）, got %v", rt.posts)
	}
	if len(rt.leverQueries) != 2 {
		t.Fatalf("dual 应读 isolated+cross 各一次, got %v", rt.leverQueries)
	}
	// ② single 模式：不带 dual_side 单读。
	rt2 := &d472GateRT{accounts: `{"available":"800","total":"800","position_mode":"single"}`, mark: "1000", levers: map[string]string{"isolated": "10"}}
	e2 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e2.client = &http.Client{Transport: rt2}
	if err := e2.MarginPrecheck(context.Background(), Order{Symbol: "XPT_USDT", Side: "SELL", Qty: 1, Venue: "gate:perp"}); err != nil {
		t.Fatalf("single 单读放行, got %v", err)
	}
	for _, q := range rt2.leverQueries {
		if strings.Contains(q, "dual_side") {
			t.Fatalf("single 模式不得带 dual_side, got %q", q)
		}
	}
	// ③ 实况不可读 → fail-closed（禁兜底——配置链退役后无第二来源）。
	rt3 := &d472GateRT{accounts: `{"available":"800","total":"800","position_mode":"dual"}`, mark: "1000", leverBroken: true}
	e3 := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e3.client = &http.Client{Transport: rt3}
	if err := e3.MarginPrecheck(context.Background(), Order{Symbol: "XPT_USDT", Side: "SELL", Qty: 1, Venue: "gate:perp"}); err == nil ||
		!strings.Contains(err.Error(), "fail-closed") {
		t.Fatalf("杠杆实况不可读应 fail-closed 拒开, got %v", err)
	}
}
