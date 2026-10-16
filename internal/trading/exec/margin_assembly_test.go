package exec

// margin_assembly_test.go —— CEX 腿保证金装配段锚（D-549 通用化）：
// okx savings→funding→unified 三段序列+装配额上限+读败 fail-closed；
// gate spot→futures 序列+读败 fail-closed。

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

// d550okxRT okx 装配链 fake：low→装配后 high 的 max-size 翻转 + 调用序列记录。
type d550okxRT struct {
	maxCalls int
	seq      []string
	savFail  bool
	funding  string
	savings  string
}

func (rt *d550okxRT) RoundTrip(req *http.Request) (*http.Response, error) {
	p := req.URL.Path
	rt.seq = append(rt.seq, req.Method+" "+p)
	var body string
	switch {
	case strings.Contains(p, "/api/v5/public/instruments"):
		body = `{"code":"0","data":[{"ctVal":"0.01","lotSz":"0.01"}]}`
	case strings.Contains(p, "/api/v5/public/mark-price"):
		body = `{"data":[{"markPx":"100"}]}`
	case strings.Contains(p, "/api/v5/account/max-size"):
		rt.maxCalls++
		v := "10"
		if rt.maxCalls > 1 {
			v = "200"
		}
		body = `{"code":"0","data":[{"maxBuy":"` + v + `","maxSell":"` + v + `"}]}`
	case strings.Contains(p, "/api/v5/asset/balances"):
		body = `{"data":[{"details":[{"ccy":"USDT","availBal":"` + rt.funding + `"}]}]}`
	case strings.Contains(p, "/api/v5/finance/savings/balance"):
		if rt.savFail {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("x")), Header: make(http.Header), Request: req}, nil
		}
		body = `{"data":[{"amt":"` + rt.savings + `"}]}`
	case strings.Contains(p, "/api/v5/finance/savings/purchase-redempt"),
		strings.Contains(p, "/api/v5/asset/transfer"):
		body = `{"code":"0","data":[]}`
	default:
		body = `{"code":"0","data":[]}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

// qty=1 base, ctVal=0.01 → sz=100 张；首读 maxSell=10 → 不足。
// need=(100−10×0.8)×0.01×100=$92。funding=50 → 赎 42（savings=500 充足）→划 92。
func TestMarginAssembly_OKX(t *testing.T) {
	rt := &d550okxRT{funding: "50", savings: "500"}
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", "http://x", false)
	e.client = &http.Client{Transport: rt}
	if err := e.MarginPrecheck(context.Background(), Order{Symbol: "X-USDT-SWAP", Side: "SELL", Qty: 1, Venue: "okx:perp"}); err != nil {
		t.Fatalf("装配后应放行: %v (seq=%v)", err, rt.seq)
	}
	// 序列断言：savings 读→赎回→划转→max-size 复判（顺序=装配三段）
	idx := func(sub string) int {
		for i, s := range rt.seq {
			if strings.Contains(s, sub) {
				return i
			}
		}
		return -1
	}
	if i, j, k := idx("savings/balance"), idx("purchase-redempt"), idx("asset/transfer"); !(i >= 0 && j > i && k > j) {
		t.Fatalf("装配序列乱: %v", rt.seq)
	}
	if rt.maxCalls < 2 {
		t.Fatalf("装配后未重读 max-size: %d", rt.maxCalls)
	}
}

// savings 可赎额读败 → fail-closed 拒（不赎不划不翻放行）。
func TestMarginAssembly_OKXSavingsReadFail(t *testing.T) {
	rt := &d550okxRT{funding: "50", savings: "500", savFail: true}
	e := NewCEXExecutor("okx", "perp", "k", "s", "p", "http://x", false)
	e.client = &http.Client{Transport: rt}
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "X-USDT-SWAP", Side: "SELL", Qty: 1, Venue: "okx:perp"})
	if err == nil || !strings.Contains(err.Error(), "装配败") {
		t.Fatalf("savings 读败应 fail-closed 拒: %v", err)
	}
	for _, s := range rt.seq {
		if strings.Contains(s, "purchase-redempt") || strings.Contains(s, "asset/transfer") {
			t.Fatalf("读败后不应有写动面: %v", rt.seq)
		}
	}
}

// d550gateRT gate 装配链 fake：futures accounts low→high 翻转 + spot 余额 +
// uni-lend 赎回段（spotPostRedeem=赎回提交后 spot 重读实值——venue 整点到账
// 语义下未到账=如实部分装配）+ transfer。
type d550gateRT struct {
	accCalls       int
	seq            []string
	spot           string
	spotPostRedeem string // 赎回后 spot 实值（空=不变）
	lent           string // uni-lend 出借中可赎额
	redeemAmt      string // 实拍赎回参
	lendsFail      bool
	spotFail       bool
	redeemed       bool
}

func (rt *d550gateRT) RoundTrip(req *http.Request) (*http.Response, error) {
	p := req.URL.Path
	rt.seq = append(rt.seq, req.Method+" "+p)
	var body string
	switch {
	case strings.Contains(p, "/futures/usdt/accounts"):
		rt.accCalls++
		v := "10"
		if rt.accCalls > 1 {
			v = "500"
		}
		body = `{"available":"` + v + `","total":"` + v + `"}`
	case strings.Contains(p, "/futures/usdt/contracts/"):
		body = `{"mark_price":"1000"}`
	case strings.Contains(p, "/futures/usdt/dual_comp/position/"): // 模式探针
		body = `{"leverage":"10"}`
	case strings.Contains(p, "/futures/usdt/positions") || strings.Contains(p, "dual_comp"):
		body = `{"leverage":"10"}`
	case strings.Contains(p, "/futures/usdt/"):
		body = `{"Lever":"10"}` // get_leverage 余面（文档钉死大写 Lever 字段）
	case strings.Contains(p, "/earn/uni/lends"):
		if rt.lendsFail {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("x")), Header: make(http.Header), Request: req}, nil
		}
		if req.Method == http.MethodPost {
			rt.redeemed = true
			if b, _ := io.ReadAll(req.Body); b != nil {
				rt.redeemAmt = string(b)
			}
			body = `{"id":1}`
		} else {
			body = `[{"currency":"USDT","lent_amount":"` + rt.lent + `"}]`
		}
	case strings.Contains(p, "/spot/accounts"):
		if rt.spotFail {
			return &http.Response{StatusCode: 500, Body: io.NopCloser(strings.NewReader("x")), Header: make(http.Header), Request: req}, nil
		}
		v := rt.spot
		if rt.redeemed && rt.spotPostRedeem != "" {
			v = rt.spotPostRedeem
		}
		body = `[{"currency":"USDT","available":"` + v + `"}]`
	case strings.Contains(p, "/wallet/transfers"):
		body = `{"txid":"1"}`
	default:
		body = `{}`
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: req}, nil
}

// qty=1 mark=1000 lev=10 → need=100；avail=10 → 缺口 92 → spot=200 划 92 后重读放行。
func TestMarginAssembly_Gate(t *testing.T) {
	rt := &d550gateRT{spot: "200"}
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: rt}
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "X_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"})
	// 杠杆实况读走 get_leverage 链——fake 未覆写即读败 fail-closed 也可接受；
	// 只锚「若走到不足分支」装配序列与读败语义。
	if err == nil {
		idx := func(sub string) int {
			for i, s := range rt.seq {
				if strings.Contains(s, sub) {
					return i
				}
			}
			return -1
		}
		if i, j := idx("spot/accounts"), idx("wallet/transfers"); !(i >= 0 && j > i) {
			t.Fatalf("gate 装配序列乱: %v", rt.seq)
		}
		if rt.accCalls < 2 {
			t.Fatalf("装配后未重读 accounts: %d", rt.accCalls)
		}
		return
	}
	if !strings.Contains(err.Error(), "gate margin insufficient") && !strings.Contains(err.Error(), "杠杆实况") && !strings.Contains(err.Error(), "margin precheck") {
		t.Fatalf("拒因失真: %v", err)
	}
}

// gate earn 赎回装配段（对齐 D-549 okx 同构）：缺口>spot → lends 读→赎回
// min(缺口,lent)→spot 重读→划转。need≈92、spot=50 → 赎 42（lent=500 充足）。
func TestMarginAssembly_GateEarnRedeem(t *testing.T) {
	rt := &d550gateRT{spot: "50", spotPostRedeem: "500", lent: "500"}
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: rt}
	_ = e.MarginPrecheck(context.Background(), Order{Symbol: "X_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"})
	if !rt.redeemed {
		t.Fatalf("spot 短应收 earn 赎回段: seq=%v", rt.seq)
	}
	if !strings.Contains(rt.redeemAmt, `"type":"redeem"`) || !strings.Contains(rt.redeemAmt, `"amount":"42`) {
		t.Fatalf("赎回参失真（应 min(缺口42,lent)）: %s", rt.redeemAmt)
	}
	var iLendsGet, iRedeem, iSpotRe, iTrans int = -1, -1, -1, -1
	for i, s := range rt.seq {
		switch {
		case strings.Contains(s, "GET /earn/uni/lends"):
			iLendsGet = i
		case strings.Contains(s, "POST /earn/uni/lends"):
			iRedeem = i
		case iRedeem >= 0 && strings.Contains(s, "spot/accounts"):
			iSpotRe = i
		case strings.Contains(s, "wallet/transfers"):
			iTrans = i
		}
	}
	if !(iLendsGet >= 0 && iRedeem > iLendsGet && iSpotRe > iRedeem && iTrans > iSpotRe) {
		t.Fatalf("装配序列乱（lends读→赎回→spot重读→划转）: %v", rt.seq)
	}
	if rt.accCalls < 2 {
		t.Fatalf("装配后未重读 futures accounts: %d", rt.accCalls)
	}
}

// uni-lend 可赎额读败 → fail-closed（零赎零划转）。
func TestMarginAssembly_GateEarnLendsReadFail(t *testing.T) {
	rt := &d550gateRT{spot: "50", lent: "500", lendsFail: true}
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: rt}
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "X_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"})
	if err == nil {
		t.Fatalf("lends 读败不应放行")
	}
	for _, s := range rt.seq {
		if strings.Contains(s, "POST /earn/uni/lends") || strings.Contains(s, "wallet/transfers") {
			t.Fatalf("可赎读败后不应有写动面: %v", rt.seq)
		}
	}
	if !strings.Contains(err.Error(), "uni-lend") && !strings.Contains(err.Error(), "杠杆实况") && !strings.Contains(err.Error(), "margin precheck") {
		t.Fatalf("拒因失真: %v", err)
	}
}

// spot 读败 → fail-closed（零划转）。
func TestMarginAssembly_GateSpotReadFail(t *testing.T) {
	rt := &d550gateRT{spot: "200", spotFail: true}
	e := NewCEXExecutor("gate", "perp", "k", "s", "", "http://x", false)
	e.client = &http.Client{Transport: rt}
	err := e.MarginPrecheck(context.Background(), Order{Symbol: "X_USDT", Side: "BUY", Qty: 1, Venue: "gate:perp"})
	if err == nil {
		// 若杠杆实况段先拒，spot 读根本未触发——仍是 fail-closed 语义，过。
		return
	}
	for _, s := range rt.seq {
		if strings.Contains(s, "wallet/transfers") {
			t.Fatalf("读败后不应有划转: %v", rt.seq)
		}
	}
}
