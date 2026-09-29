package exchange

// d570c_gate_test.go —— D-570 包C 验收锚 A12-A16（collect 侧：contract_info 失效/
// 私有 5 频道整帧转发/adl_warning 告警/book_ticker 盘口/白名单订阅集）。
import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
)

// A12③④：contract_info 帧（update+all 同路）→ onContractInfo 逐合约回调；
// GatePrivateWS.InvalidateQuanto 删实例缓存键。
func TestD570C_A12_ContractInfoInvalidate(t *testing.T) {
	var got []string
	g := &GateWebSocket{}
	g.SetOnContractInfo(func(c string) { got = append(got, c) })
	// update 帧数组形（快照 :3380）。
	g.handleFuturesDispatch(context.Background(), []byte(
		`{"channel":"futures.contract_info","event":"update","result":[{"contract":"BTC_USDT","quanto_multiplier":"0.0001"},{"name":"ETH_USDT"}]}`), func(bus.Quote) {})
	// all 首帧单对象形同路。
	g.handleFuturesDispatch(context.Background(), []byte(
		`{"channel":"futures.contract_info","event":"all","result":{"contract":"SOL_USDT"}}`), func(bus.Quote) {})
	if len(got) != 3 || got[0] != "BTC_USDT" || got[1] != "ETH_USDT" || got[2] != "SOL_USDT" {
		t.Fatalf("contract_info 应逐合约回调(contract/name 双容): %v", got)
	}
	// nil 回调安全（未接线不崩）。
	(&GateWebSocket{}).onContractInfoFrame([]byte(`{"result":[{"contract":"X_USDT"}]}`))
	// InvalidateQuanto：collect 侧 quanto 键删除（公共 WS 失效链触达）。
	p := &GatePrivateWS{quanto: map[string]float64{"BTC_USDT": 0.0001}}
	p.InvalidateQuanto("BTC_USDT")
	if _, ok := p.quanto["BTC_USDT"]; ok {
		t.Fatalf("InvalidateQuanto 后 quanto 仍命中")
	}
	p.InvalidateQuanto("MISS_USDT") // 无键不崩
}

// A13：私有订阅面 9 频道（旧4+新5）；subscribeFrame 各带 channel 名+签名；
// liquidates 帧→OnLiquidate payload=原文+audit 行在。
func TestD570C_A13_PrivateChannelsAndHooks(t *testing.T) {
	if len(gatePrivateChannels) != 9 {
		t.Fatalf("订阅面应 9 频道（4+5）: %d", len(gatePrivateChannels))
	}
	want := map[string]bool{
		"futures.liquidates": false, "futures.position_closes": false,
		"futures.auto_deleverages": false, "futures.positions_adl": false,
		"futures.reduce_risk_limits": false,
	}
	g := &GatePrivateWS{apiKey: "K", apiSecret: "S", now: func() time.Time { return time.Unix(1700000000, 0) }}
	for _, ch := range gatePrivateChannels {
		f := g.subscribeFrame(ch, "42")
		if f["channel"] != ch || f["event"] != "subscribe" {
			t.Fatalf("%s 帧缺 channel/event", ch)
		}
		auth, _ := f["auth"].(map[string]string)
		if auth["SIGN"] == "" || auth["SIGN"] != gateWSSign("S", ch, 1700000000) {
			t.Fatalf("%s 签名缺/不符（签名串须含 channel 名）", ch)
		}
		pl, _ := f["payload"].([]string)
		if ch == "futures.balances" {
			if len(pl) != 1 || pl[0] != "42" {
				t.Fatalf("balances payload 应 [uid]: %v", pl)
			}
		} else if len(pl) != 2 || pl[0] != "42" || pl[1] != "!all" {
			t.Fatalf("%s payload 应 [uid,!all]: %v", ch, pl)
		}
		if _, ok := want[ch]; ok {
			want[ch] = true
		}
	}
	for ch, seen := range want {
		if !seen {
			t.Fatalf("新频道 %s 不在订阅面", ch)
		}
	}
	// 帧转发：liquidates→OnLiquidate 原文+audit 行。
	var hookPayload string
	evts := newFakeEventSink()
	g.events = evts
	g.SetGateHooks(GatePrivateHooks{OnLiquidate: func(p []byte) { hookPayload = string(p) }})
	raw := `{"channel":"futures.liquidates","event":"update","result":[{"contract":"BTC_USDT","size":-3,"liq_price":"60000"}]}`
	if err := g.handleFrame(context.Background(), []byte(raw)); err != nil {
		t.Fatalf("handleFrame: %v", err)
	}
	if !strings.Contains(hookPayload, `"liq_price":"60000"`) {
		t.Fatalf("OnLiquidate 应收 result 原文: %q", hookPayload)
	}
	found := false
	for _, r := range evts.rows {
		if strings.Contains(r, "futures.liquidates") {
			found = true
		}
	}
	if !found {
		t.Fatalf("audit 应含 futures.liquidates 行: %v", evts.rows)
	}
	// 其余四频道同槽断言（漏一 case→对应 hook 断言红）。
	cases := map[string]*string{
		"futures.position_closes":    new(string),
		"futures.auto_deleverages":   new(string),
		"futures.positions_adl":      new(string),
		"futures.reduce_risk_limits": new(string),
	}
	g2 := &GatePrivateWS{now: func() time.Time { return time.Now() }}
	g2.SetGateHooks(GatePrivateHooks{
		OnPositionClose: func(p []byte) { *cases["futures.position_closes"] = string(p) },
		OnADL:           func(p []byte) { *cases["__last__"] = string(p) },
		OnRiskLimit:     func(p []byte) { *cases["futures.reduce_risk_limits"] = string(p) },
	})
	last := ""
	cases["__last__"] = &last
	for ch, dst := range cases {
		if ch == "__last__" {
			continue
		}
		frame := `{"channel":"` + ch + `","event":"update","result":[{"contract":"BTC_USDT","tag":"` + ch + `"}]}`
		if err := g2.handleFrame(context.Background(), []byte(frame)); err != nil {
			t.Fatalf("%s handleFrame: %v", ch, err)
		}
		if ch == "futures.auto_deleverages" || ch == "futures.positions_adl" {
			if !strings.Contains(last, `"tag":"`+ch+`"`) {
				t.Fatalf("%s 应走 OnADL 同槽: %q", ch, last)
			}
		} else if !strings.Contains(*dst, `"tag":"`+ch+`"`) {
			t.Fatalf("%s hook 未触达", ch)
		}
	}
}

// A14：公共订阅帧含 futures.adl_warning ["!all"]；state=warning→AlertFn critical 截 500。
func TestD570C_A14_ADLWarning(t *testing.T) {
	found := false
	for _, f := range gateFuturesExtraSubs() {
		m, _ := f.(map[string]any)
		if m["channel"] == "futures.adl_warning" {
			pl, _ := m["payload"].([]string)
			if len(pl) != 1 || pl[0] != "!all" {
				t.Fatalf("adl_warning payload 应 [!all]: %v", pl)
			}
			found = true
		}
	}
	if !found {
		t.Fatalf("futures 订阅面缺 futures.adl_warning")
	}
	var level, msg string
	g := &GateWebSocket{cfg: Config{AlertFn: func(l, m string) { level, msg = l, m }}}
	big := strings.Repeat("x", 600)
	g.handleFuturesDispatch(context.Background(), []byte(
		`{"channel":"futures.adl_warning","event":"update","result":{"contract":"BTC_USDT","state":"warning","pad":"`+big+`"}}`), func(bus.Quote) {})
	if level != "critical" || !strings.Contains(msg, "gate adl_warning: ") {
		t.Fatalf("warning 帧应 critical: level=%q msg=%q", level, msg)
	}
	if len(msg) > len("gate adl_warning: ")+500 {
		t.Fatalf("payload 应截 500: len=%d", len(msg))
	}
	// state=normal 不告。
	level, msg = "", ""
	g.handleFuturesDispatch(context.Background(), []byte(
		`{"channel":"futures.adl_warning","event":"update","result":{"contract":"BTC_USDT","state":"normal"}}`), func(bus.Quote) {})
	if msg != "" {
		t.Fatalf("normal 帧不应告警: %q", msg)
	}
}

// A15：book_ticker 真形帧→Bid=b/Ask=a 真盘口（≠mark 同值）；a="" 空边 skip；
// tickers mark 兜底共存（两源均发布，键同 fresher wins）。
func TestD570C_A15_BookTicker(t *testing.T) {
	var quotes []bus.Quote
	g := &GateWebSocket{}
	pub := func(q bus.Quote) { quotes = append(quotes, q) }
	g.handleFuturesDispatch(context.Background(), []byte(
		`{"channel":"futures.book_ticker","event":"update","result":{"t":1700000000000,"u":1,"s":"BTC_USDT","b":"64999.5","B":"10","a":"65000.5","A":"8"}}`), pub)
	if len(quotes) != 1 {
		t.Fatalf("book_ticker 应发布一条: %d", len(quotes))
	}
	q := quotes[0]
	if q.Bid != 64999.5 || q.Ask != 65000.5 || q.Bid == q.Ask {
		t.Fatalf("应真实盘口 Bid≠Ask（b/a 原生）: %+v", q)
	}
	if q.Venue != VenueGate || q.VenueType != "perp" || q.VenueSymbol != "BTC_USDT" || q.Symbol != "BTCUSDT" {
		t.Fatalf("venue 键错: %+v", q)
	}
	if q.Time.UnixMilli() != 1700000000000 {
		t.Fatalf("ts 应取帧 t(ms): %v", q.Time)
	}
	// 空边帧 skip 不崩不发布。
	g.handleBookTicker([]byte(
		`{"event":"update","result":{"s":"ETH_USDT","b":"","B":"0","a":"3001","A":"5"}}`), pub)
	if len(quotes) != 1 {
		t.Fatalf("空边帧应 skip: %d", len(quotes))
	}
	// tickers mark 兜底共存（同连接两源不互斥）。
	g.handleFuturesDispatch(context.Background(), []byte(
		`{"channel":"futures.tickers","event":"update","result":[{"contract":"BTC_USDT","mark_price":"65010","t":1700000001000}]}`), pub)
	if len(quotes) != 2 || quotes[1].Bid != quotes[1].Ask || quotes[1].Bid != 65010 {
		t.Fatalf("mark 兜底路径应保留（Bid=Ask=mark）: %+v", quotes[1:])
	}
}

// A16：book_ticker 订阅列表=wl.filter 后集——同 pairs 集对所有 pairs 频道一帧，
// 非法 pair 不进帧。
func TestD570C_A16_WhitelistSameSet(t *testing.T) {
	filtered := []string{"BTC_USDT", "ETH_USDT"} // wl.filter 后集（BAD_USDT 已滤）
	frames := gateSubFrames(gateFuturesTickerChannels, filtered, nil)
	if len(frames) != 2 {
		t.Fatalf("应对每频道一帧: %d", len(frames))
	}
	seen := map[string]bool{}
	for _, f := range frames {
		m, _ := f.(map[string]any)
		pl, _ := m["payload"].([]string)
		seen[m["channel"].(string)] = true
		if len(pl) != 2 {
			t.Fatalf("%s payload 应=过滤后集: %v", m["channel"], pl)
		}
		for _, p := range pl {
			if p == "BAD_USDT" {
				t.Fatalf("非法 pair 不应进 %s 帧", m["channel"])
			}
		}
	}
	if !seen["futures.book_ticker"] || !seen["futures.tickers"] {
		t.Fatalf("两 pairs 频道应同集: %v", seen)
	}
}
