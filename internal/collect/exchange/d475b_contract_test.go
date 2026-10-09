// d475b_contract_test.go —— D-475b S1：WS→cache 字段对拍契约测试（测试即契约）。
// 每 venue×channel 一张契约表：venue 原生 WS 帧 fixture → 处理器 → cache 读出 ≡
// REST 响应 fixture → reconcile.CEXPositionQuerier/CEXAccountQuerier（权威基线）→ 输出，
// 逐字段对拍。declared-skip 清单显式声明原因——未在清单的字段回归即红。
// fixture 字段名以 venue 官方私有频道 schema + cex_private_events 审计表实帧口径为准。
package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arb/internal/exec"
	"arb/internal/httpx"
	"arb/internal/reconcile"
	"arb/internal/store/pgstore"
	"arb/internal/venue"
)

// d475bSink 捕获 snapshot 写入（balance 通道对拍面）。
type d475bSink struct {
	rows []pgstore.VenueAccountSnapshot
}

func (s *d475bSink) InsertVenueAccountSnapshots(_ context.Context, r []pgstore.VenueAccountSnapshot) error {
	s.rows = append(s.rows, r...)
	return nil
}

var d475bNow = func() time.Time { return time.Unix(1700000000, 0) }

// diffVenueOrder 逐字段对拍 WS vs REST venue.Order；skip 清单外字段不等即 fail。
func diffVenueOrder(t *testing.T, tag string, ws, rest venue.Order, skip map[string]string) {
	t.Helper()
	cmp := []struct {
		name string
		w, r any
	}{
		{"Ticket", ws.Ticket, rest.Ticket},
		{"Symbol", ws.Symbol, rest.Symbol},
		{"Side", ws.Side, rest.Side},
		{"PosSide", ws.PosSide, rest.PosSide},
		{"Volume", ws.Volume, rest.Volume},
		{"OpenPrice", ws.OpenPrice, rest.OpenPrice},
		{"Profit", ws.Profit, rest.Profit},
		{"Commission", ws.Commission, rest.Commission},
		{"InitialMargin", ws.InitialMargin, rest.InitialMargin},
		{"MaintMargin", ws.MaintMargin, rest.MaintMargin},
	}
	for _, c := range cmp {
		if reason, ok := skip[c.name]; ok {
			t.Logf("%s skip %s: %s", tag, c.name, reason)
			continue
		}
		if c.w != c.r {
			t.Errorf("%s 字段 %s 对拍不等: WS=%v REST=%v", tag, c.name, c.w, c.r)
		}
	}
	if _, ok := skip["OpenedAt"]; !ok && !ws.OpenedAt.Equal(rest.OpenedAt) {
		t.Errorf("%s OpenedAt 不等: WS=%v REST=%v", tag, ws.OpenedAt, rest.OpenedAt)
	}
}

func d475bREST(t *testing.T, venue, path, body string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
}

// ==== positions 三所对拍 ====

// TestD475b_Contract_OKXPositions okx positions 通道全字段对拍（upl/fee/cTime/imr/mmr 全消费=零 skip）。
func TestD475b_Contract_OKXPositions(t *testing.T) {
	srv := d475bREST(t, "okx", "", `{"code":"0","data":[{"instId":"XAU-USDT-SWAP","posSide":"short","pos":"1016","avgPx":"3900.5","upl":"-12.34","fee":"-1.5","imr":"400","margin":"390","mmr":"350","cTime":"1700000000000"}]}`)
	defer srv.Close()
	restQ := reconcile.NewCEXPositionQuerier("okx", "perp", "k", "s", "p", srv.URL, false)
	rest, err := restQ.QueryOpenedOrders(context.Background())
	if err != nil || len(rest) != 1 {
		t.Fatalf("rest: %v n=%d", err, len(rest))
	}
	pc := NewPositionEventCache()
	o := &OKXPrivateWS{venueKey: "okx:perp", posCache: pc, now: d475bNow}
	// venue 原生 positions 帧（WS 推送 data[] 与 REST data[] 同 schema——OKX 官方同源）。
	o.onPositions([]map[string]any{{
		"instId": "XAU-USDT-SWAP", "posSide": "short", "pos": "1016", "avgPx": "3900.5",
		"upl": "-12.34", "fee": "-1.5", "imr": "400", "margin": "390", "mmr": "350", "cTime": "1700000000000",
	}})
	ws, ok := pc.get("okx:perp", time.Hour, d475bNow())
	if !ok || len(ws) != 1 {
		t.Fatalf("ws cache: %v n=%d", ok, len(ws))
	}
	diffVenueOrder(t, "okx.positions", ws[0], rest[0], nil)
}

// TestD475b_Contract_GatePositions gate positions：WS 只消费 contract/size/entry_price/margin。
// skip 清单=实测字段缺漏（缺口清单条目——不修，呈复审）。
func TestD475b_Contract_GatePositions(t *testing.T) {
	srv := d475bREST(t, "gate", "", "")
	defer srv.Close()
	// contracts spec 端点供两侧 quanto（0.001）。
	mux := http.NewServeMux()
	mux.HandleFunc("/futures/usdt/contracts/BTC_USDT", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"quanto_multiplier":"0.001"}`))
	})
	mux.HandleFunc("/futures/usdt/positions", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[{"contract":"BTC_USDT","size":-2000,"entry_price":"60000","unrealised_pnl":"-30","pnl_fund":"-1.2","initial_margin":"","margin":"120","maintenance_margin":"100"}]`))
	})
	srv2 := httptest.NewServer(mux)
	defer srv2.Close()
	restQ := reconcile.NewCEXPositionQuerier("gate", "perp", "k", "s", "", srv2.URL, false)
	rest, err := restQ.QueryOpenedOrders(context.Background())
	if err != nil || len(rest) != 1 {
		t.Fatalf("rest: %v n=%d", err, len(rest))
	}
	pc := NewPositionEventCache()
	g := &GatePrivateWS{baseURL: srv2.URL, venueKey: "gate:perp", posCache: pc, rest: srv2.Client(), now: d475bNow, quanto: map[string]float64{}}
	// D-477 S2：帧补全 GAP-B2 字段（unrealised_pnl/pnl_fund/maintenance_margin/open_time）。
	g.onPositions(json.RawMessage(`[{"contract":"BTC_USDT","size":-2000,"entry_price":"60000","margin":"120","unrealised_pnl":"-30","pnl_fund":"-1.2","maintenance_margin":"100","open_time":1790186326}]`))
	ws, ok := pc.get("gate:perp", time.Hour, d475bNow())
	if !ok || len(ws) != 1 {
		t.Fatalf("ws cache: %v n=%d", ok, len(ws))
	}
	// D-477 S2：GAP-B2 字段全消费=skip 摘除转正断言；OpenedAt 保留 skip（REST gate 无
	// 开仓时刻字段——WS=帧 open_time 秒级 epoch，与 REST now 不等属预期非回归）。
	diffVenueOrder(t, "gate.positions", ws[0], rest[0], map[string]string{
		"OpenedAt": "REST gate 无开仓时刻字段（WS=帧 open_time，不等属预期）",
	})
}

// TestD475b_Contract_BinancePositions binance ACCOUNT_UPDATE a.P 段对拍。
// D-477 S1：up 已消费→Profit、Ticket=symbol——GAP-B1 两 skip 摘除转正断言。
func TestD475b_Contract_BinancePositions(t *testing.T) {
	srv := d475bREST(t, "binance", "", `[{"symbol":"BTCUSDT","positionAmt":"-0.5","entryPrice":"60000","unRealizedProfit":"-30","positionSide":"SHORT","marginType":"isolated","isolatedWallet":"100","leverage":"10","notional":"-30000"}]`)
	defer srv.Close()
	restQ := reconcile.NewCEXPositionQuerier("binance", "perp", "k", "s", "", srv.URL, false)
	rest, err := restQ.QueryOpenedOrders(context.Background())
	if err != nil || len(rest) != 1 {
		t.Fatalf("rest: %v n=%d", err, len(rest))
	}
	pc := NewPositionEventCache()
	b := &BinanceUserWS{venueKey: "binance:perp", venueType: "perp", posCache: pc, now: d475bNow, simulated: true}
	// a.P 段 venue 原生键：s=symbol pa=positionAmt ep=entryPrice iw=isolatedWallet ps=positionSide up=unrealized。
	b.onAccountUpdate(context.Background(), json.RawMessage(`{"m":"ORDER","B":[],"P":[{"s":"BTCUSDT","pa":"-0.5","ep":"60000","iw":"100","ps":"SHORT","up":"-30"}]}`))
	ws, ok := pc.get("binance:perp", time.Hour, d475bNow())
	if !ok || len(ws) != 1 {
		t.Fatalf("ws cache: %v n=%d", ok, len(ws))
	}
	// OpenedAt 保留 skip：REST positionRisk 无开仓时刻字段（两侧语义不同源）。
	diffVenueOrder(t, "binance.positions", ws[0], rest[0], map[string]string{
		"OpenedAt": "REST positionRisk 无开仓时刻字段",
	})
}

// ==== orders 两所对拍（gate 无 REST OrderStatusQuerier→整行 declared-skip） ====

func d475bOrderCacheGet(c *OrderEventCache, venueKey, orderID string) (OrderEvent, bool) {
	return c.get(venueKey, orderID, "", time.Hour, d475bNow())
}

func TestD475b_Contract_OKXOrders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v5/public/instruments"):
			_, _ = w.Write([]byte(`{"code":"0","data":[{"ctVal":"0.01","lotSz":"0.01"}]}`))
		default:
			_, _ = w.Write([]byte(`{"code":"0","data":[{"instId":"XAU-USDT-SWAP","ordId":"ord1","clOrdId":"cl1","state":"filled","accFillSz":"2","avgPx":"3900.5","fee":"-0.5","feeCcy":"USDT"}]}`))
		}
	}))
	defer srv.Close()
	oc := NewOrderEventCache()
	o := &OKXPrivateWS{venueKey: "okx:perp", orderCache: oc, now: d475bNow}
	// D-477 S3（GAP-B4）：ctValFor 注入与 REST querier 同 baseURL 源——双源分叉撤除，
	// qty 对拍由 skip 转正断言（accFillSz=2 张 × ctVal=0.01 = 0.02 base 两侧同值）。
	o.SetCtValFor(func(ctx context.Context, instID string) (float64, error) {
		ctVal, _, _, err := exec.OkxSwapSpec(ctx, httpx.Client(), srv.URL, instID)
		return ctVal, err
	})
	o.onOrders(context.Background(), []map[string]any{{
		"instId": "XAU-USDT-SWAP", "ordId": "ord1", "clOrdId": "cl1", "state": "filled",
		"accFillSz": "2", "avgPx": "3900.5", "fee": "-0.5", "feeCcy": "USDT",
	}})
	wsEv, ok := d475bOrderCacheGet(oc, "okx:perp", "ord1")
	if !ok {
		t.Fatal("ws orderCache 无行")
	}
	// REST 基线=CEXExecutor.QueryOrderStatus（同 GET /api/v5/trade/order，同源 ctVal）。
	ex := newOKXStatusQuerier(srv.URL)
	st, qty, px, fee, err := ex.QueryOrderStatus(context.Background(), "ord1", "cl1", "XAU-USDT-SWAP")
	if err != nil {
		t.Fatalf("rest order: %v", err)
	}
	if wsEv.State != st || wsEv.AvgPx != px || wsEv.FeeUSD != fee {
		t.Fatalf("orders 对拍不等: WS=%+v REST=(%s,_,%v,%v)", wsEv, st, px, fee)
	}
	if wsEv.AccFillQty != qty {
		t.Fatalf("单源断言：AccFillQty WS=%v REST=%v（同 ctVal 源应相等）", wsEv.AccFillQty, qty)
	}
}

func TestD475b_Contract_BinanceOrders(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"FILLED","executedQty":"0.5","avgPrice":"60000"}`))
	}))
	defer srv.Close()
	oc := NewOrderEventCache()
	b := &BinanceUserWS{venueKey: "binance:perp", orderCache: oc, now: d475bNow}
	b.onOrderUpdate(json.RawMessage(`{"i":777,"c":"cl1","X":"FILLED","z":"0.5","ap":"60000","L":"60000","n":"0.02"}`))
	wsEv, ok := d475bOrderCacheGet(oc, "binance:perp", "777")
	if !ok {
		t.Fatal("ws orderCache 无行")
	}
	ex := newBinanceStatusQuerier(srv.URL)
	st, qty, px, fee, err := ex.QueryOrderStatus(context.Background(), "777", "", "BTCUSDT")
	if err != nil {
		t.Fatalf("rest order: %v", err)
	}
	// FeeUSD 对拍跳过=B5 裁案（D-477 S4）：REST order-status 端点本身不回传手续费，
	// 返 0=按设计非缺漏；fee 真值权威源=income 事件流（venue 原生）。
	if wsEv.State != st || wsEv.AccFillQty != qty || wsEv.AvgPx != px {
		t.Fatalf("orders 对拍不等: WS=%+v REST=(%s,%v,%v)", wsEv, st, qty, px)
	}
	if fee != 0 {
		t.Logf("REST fee=%v（按设计=0；fee 真值=income 事件流，B5 裁案固化）", fee)
	}
}

// ==== account/balance 三所对拍 ====

func TestD475b_Contract_Accounts(t *testing.T) {
	ctx := context.Background()
	// okx account push vs accountOKX：eqUsd 求和同口径。
	okxSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"code":"0","data":[{"mgnRatio":"","details":[{"ccy":"USDT","eqUsd":"1000.5","cashBal":"1000.5","availBal":"900"}]}]}`))
	}))
	defer okxSrv.Close()
	rest, err := reconcile.NewCEXAccountQuerier("okx", "unified", "k", "s", "p", okxSrv.URL, "", false).Account(ctx)
	if err != nil {
		t.Fatalf("okx account rest: %v", err)
	}
	row, _, ok := okxAccountPushRow(map[string]any{"mgnRatio": "", "details": []any{
		map[string]any{"ccy": "USDT", "eqUsd": "1000.5", "cashBal": "1000.5", "availBal": "900"}}}, d475bNow())
	if !ok {
		t.Fatal("okxAccountPushRow false")
	}
	if row.EquityRaw != rest.Equity || row.BalanceRaw != rest.Balance {
		t.Fatalf("okx account 对拍: WS=%v/%v REST=%v/%v", row.EquityRaw, row.BalanceRaw, rest.Equity, rest.Balance)
	}
	// gate futures.balances vs accountGate（total↔balance）。
	gateSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"total":"888.8"}`))
	}))
	defer gateSrv.Close()
	grest, err := reconcile.NewCEXAccountQuerier("gate", "perp", "k", "s", "", gateSrv.URL, "", false).Account(ctx)
	if err != nil {
		t.Fatalf("gate account rest: %v", err)
	}
	gsink := &d475bSink{}
	g := &GatePrivateWS{venueKey: "gate:perp", sink: gsink, now: d475bNow}
	g.onBalances(ctx, json.RawMessage(`[{"currency":"USDT","balance":"888.8"}]`))
	if len(gsink.rows) != 1 || gsink.rows[0].EquityRaw != grest.Equity {
		t.Fatalf("gate balances 对拍: %+v vs REST equity=%v", gsink.rows, grest.Equity)
	}
	// binance D-142：WS a.B wb+Σup vs REST totalMarginBalance——同口径对拍（777.7+2.3=780）。
	binSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"totalWalletBalance":"777.7","totalMarginBalance":"780","totalMaintMargin":"100"}`))
	}))
	defer binSrv.Close()
	brest, err := reconcile.NewCEXAccountQuerier("binance", "perp", "k", "s", "", binSrv.URL, "", false).Account(ctx)
	if err != nil {
		t.Fatalf("binance account rest: %v", err)
	}
	bsink := &d475bSink{}
	b := &BinanceUserWS{venueKey: "binance:perp", venueType: "perp", sink: bsink, now: d475bNow, posCache: NewPositionEventCache(), balLedger: nil}
	b.onAccountUpdate(ctx, json.RawMessage(`{"m":"ORDER","B":[{"a":"USDT","wb":"777.7"}],"P":[{"s":"TRXUSDT","pa":"16","ep":"0.334","iw":"5.3","ps":"BOTH","up":"2.3"}]}`))
	if len(bsink.rows) != 1 || bsink.rows[0].EquityRaw != brest.Equity {
		t.Fatalf("binance balances 对拍: %+v vs REST equity=%v", bsink.rows, brest.Equity)
	}
}

// REST 订单基线构造（exec.CEXExecutor.QueryOrderStatus——该接口即 REST 权威读面）。
func newOKXStatusQuerier(baseURL string) *exec.CEXExecutor {
	return exec.NewCEXExecutor("okx", "perp", "k", "s", "p", baseURL, false)
}
func newBinanceStatusQuerier(baseURL string) *exec.CEXExecutor {
	return exec.NewCEXExecutor("binance", "perp", "k", "s", "", baseURL, false)
}
