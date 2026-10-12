package exec

// 验收锚：S2 spot 现金预检 + KB spot lotSz 地板 + S4 平仓腿向。
// 现金预检走 httptest 伪 venue（字段名按真实响应形状；SELL 腿零请求实证 = 断网也放行）。

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

// === S2 spot 现金预检 ===

// okxSpotServer 伪 OKX：ticker + balance（availBal 可调）。
func okxSpotServer(t *testing.T, availBal string, hits *int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if hits != nil {
			atomic.AddInt32(hits, 1)
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/account/config":
			w.Write([]byte(`{"code":"0","data":[{"posMode":"long_short_mode"}]}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/api/v5/public/instruments"):
			w.Write([]byte(`{"code":"0","data":[{"ctVal":"1000","lotSz":"0.01","state":"live"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v5/market/ticker"):
			w.Write([]byte(`{"code":"0","data":[{"last":"0.50"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v5/account/balance"):
			w.Write([]byte(`{"code":"0","data":[{"details":[{"ccy":"USDT","availBal":"` + availBal + `"}]}]}`))
		default:
			t.Errorf("unexpected okx path %s", r.URL.Path)
		}
	}))
}

func TestD473_SpotPrecheck_OKX_BuyPassAndReject(t *testing.T) {
	srv := okxSpotServer(t, "200", nil) // 余额 200 USDT
	defer srv.Close()
	ex := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, false)
	ex.SetSpotTakerBps(10.0)
	ex.SetMarginReservePct(0)
	ctx := context.Background()
	// qty 100 × mark 0.50 × (1+10bps) = 50.05 < 200 → 过
	if err := ex.MarginPrecheck(ctx, Order{Symbol: "SKL-USDT", Side: "BUY", Qty: 100}); err != nil {
		t.Fatalf("足余额应放行, got %v", err)
	}
	// qty 10000 × 0.5 = 5005 > 200 → 拒（fail-closed error）
	if err := ex.MarginPrecheck(ctx, Order{Symbol: "SKL-USDT", Side: "BUY", Qty: 10000}); err == nil {
		t.Fatal("余额不足应拒开")
	}
}

func TestD473_SpotPrecheck_OKX_SellSkips(t *testing.T) {
	var hits int32
	srv := okxSpotServer(t, "0", &hits) // 余额 0 也不该被问
	defer srv.Close()
	ex := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, false)
	if err := ex.MarginPrecheck(context.Background(), Order{Symbol: "SKL-USDT", Side: "SELL", Qty: 10000}); err != nil {
		t.Fatalf("SELL 腿不查现金, got %v", err)
	}
	if hits != 0 {
		t.Fatalf("SELL 腿应零请求, got %d", hits)
	}
}

func TestD473_SpotPrecheck_OKX_BalanceUnreachable(t *testing.T) {
	// 只有 ticker 无 balance → 取不到余额 fail-closed
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/api/v5/market/ticker") {
			w.Write([]byte(`{"code":"0","data":[{"last":"0.50"}]}`))
			return
		}
		w.WriteHeader(500)
	}))
	defer srv.Close()
	ex := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, false)
	if err := ex.MarginPrecheck(context.Background(), Order{Symbol: "SKL-USDT", Side: "BUY", Qty: 1}); err == nil {
		t.Fatal("余额端点取不到应 fail-closed 拒开")
	}
}

func TestD473_SpotPrecheck_Binance(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v3/ticker/price"):
			w.Write([]byte(`{"symbol":"SKLUSDT","price":"0.50"}`))
		case strings.HasPrefix(r.URL.Path, "/v3/account"):
			w.Write([]byte(`{"balances":[{"asset":"SKL","free":"999"},{"asset":"USDT","free":"100"}]}`))
		default:
			t.Errorf("unexpected binance path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	ex := NewCEXExecutor("binance", "spot", "k", "s", "", srv.URL, false)
	ctx := context.Background()
	if err := ex.MarginPrecheck(ctx, Order{Symbol: "SKLUSDT", Side: "BUY", Qty: 100}); err != nil { // 50 < 100
		t.Fatalf("足余额应放行, got %v", err)
	}
	if err := ex.MarginPrecheck(ctx, Order{Symbol: "SKLUSDT", Side: "BUY", Qty: 1000}); err == nil { // 500 > 100
		t.Fatal("余额不足应拒开")
	}
	// 报价币行缺 → 不猜
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/v3/ticker/price") {
			w.Write([]byte(`{"price":"0.50"}`))
			return
		}
		w.Write([]byte(`{"balances":[{"asset":"SKL","free":"999"}]}`))
	}))
	defer srv2.Close()
	ex2 := NewCEXExecutor("binance", "spot", "k", "s", "", srv2.URL, false)
	if err := ex2.MarginPrecheck(ctx, Order{Symbol: "SKLUSDT", Side: "BUY", Qty: 1}); err == nil {
		t.Fatal("USDT 行缺应 fail-closed")
	}
}

func TestD473_SpotPrecheck_Gate(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/spot/tickers"):
			w.Write([]byte(`[{"currency_pair":"SKL_USDT","last":"0.50"}]`))
		case strings.HasPrefix(r.URL.Path, "/spot/accounts"):
			w.Write([]byte(`[{"currency":"USDT","available":"100","locked":"0"}]`))
		default:
			t.Errorf("unexpected gate path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	ex := NewCEXExecutor("gate", "spot", "k", "s", "", srv.URL, false)
	ctx := context.Background()
	if err := ex.MarginPrecheck(ctx, Order{Symbol: "SKL_USDT", Side: "BUY", Qty: 100}); err != nil { // 50 < 100
		t.Fatalf("足余额应放行, got %v", err)
	}
	if err := ex.MarginPrecheck(ctx, Order{Symbol: "SKL_USDT", Side: "BUY", Qty: 1000}); err == nil { // 500 > 100
		t.Fatal("余额不足应拒开")
	}
}

func TestD473_SpotQuoteCcy(t *testing.T) {
	cases := []struct {
		venue, symbol, want string
		fail                bool
	}{
		{"okx", "SKL-USDT", "USDT", false},
		{"gate", "SKL_USDT", "USDT", false},
		{"binance", "SKLUSDT", "USDT", false},
		{"binance", "SKLUSDC", "USDC", false},
		{"binance", "SKLBTC", "", true}, // 不识报价币→fail
		{"okx", "SKLUSDT", "", true},    // 无分隔符→fail
	}
	for _, c := range cases {
		got, err := spotQuoteCcy(c.venue, c.symbol)
		if c.fail {
			if err == nil {
				t.Errorf("%s/%s 应解析失败, got %q", c.venue, c.symbol, got)
			}
			continue
		}
		if err != nil || got != c.want {
			t.Errorf("%s/%s = %q,%v want %q", c.venue, c.symbol, got, err, c.want)
		}
	}
}

// === KB 钉项：okx spot sz lotSz 地板 + minSz 拒单 ===

func TestD473_OKXSpotLotSzFloor(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/api/v5/public/instruments"):
			w.Write([]byte(`{"code":"0","data":[{"lotSz":"0.01","minSz":"0.01"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v5/trade/order"):
			if r.Method == http.MethodPost {
				b := make([]byte, r.ContentLength)
				r.Body.Read(b)
				bodies = append(bodies, string(b))
			}
			w.Write([]byte(`{"code":"0","data":[{"ordId":"o1","sCode":"0","avgPx":"0.5","accFillSz":"10"}]}`))
		default:
			w.Write([]byte(`{"code":"0","data":[]}`)) // fee 查询等辅助 GET 放行
		}
	}))
	defer srv.Close()
	ex := NewCEXExecutor("okx", "spot", "k", "s", "p", srv.URL, false)
	// qty 10.006 → 地板 10.00
	fill, err := ex.PlaceOrder(context.Background(), Order{Symbol: "SKL-USDT", Side: "BUY", Qty: 10.006})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if len(bodies) != 1 || !strings.Contains(bodies[0], `"sz":"10"`) {
		t.Fatalf("sz 应地板到 lotSz 10, bodies=%v", bodies)
	}
	_ = fill
	// qty 0.005 < minSz 0.01 → fail-closed 拒
	if _, err := ex.PlaceOrder(context.Background(), Order{Symbol: "SKL-USDT", Side: "BUY", Qty: 0.005}); err == nil {
		t.Fatal("qty<minSz 应拒单")
	}
}

// === S4 平仓端到端：FUNDING_HEDGE 仓 close → spot SELL 市价 + perp BUY reduceOnly ===

func TestD473_FundingHedgeCloseLegs(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	reg := NewRegistry()
	spotExe := &captureOrderExecutor{key: "okx:spot", fill: Fill{OrderID: "cs1", Qty: 10, Price: 0.5}}
	perpExe := &captureOrderExecutor{key: "okx:perp", fill: Fill{OrderID: "cp1", Qty: 10, Price: 0.51}}
	reg.Register(spotExe)
	reg.Register(perpExe)
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	// 两腿均已平（verifier 空=broker 侧无仓？不——verifier 返回在仓订单驱动 preflight，
	// 这里返回空→视为 broker 已无腿会走 backfill。要发单须 verifier 报仍有腿）。
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"okx:spot": {{Symbol: "SKL-USDT", Side: "BUY"}},
		"okx:perp": {{Symbol: "SKL-USDT-SWAP", Side: "SELL", PosSide: "short"}},
	}}

	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('FUNDING_HEDGE:SKLUSDT:okx:spot:okx:perp', 'FUNDING_HEDGE', 'CLOSING', NOW())
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	for _, l := range []pgstore.PositionLeg{
		{PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "okx:spot", Symbol: "SKL-USDT", Side: "BUY", Qty: 10, OrderID: "os1", Status: "FILLED"},
		{PositionID: posID, LegNo: 1, Phase: "OPEN", Venue: "okx:perp", Symbol: "SKL-USDT-SWAP", Side: "SELL", Qty: 10, OrderID: "op1", Status: "FILLED"},
	} {
		if err := s.CreatePositionLeg(ctx, &l); err != nil {
			t.Fatalf("seed leg: %v", err)
		}
	}

	// closePositionCore：两腿 broker 仍在仓 → 发平仓单（残量/终态校验报错不敏感，单已发）
	if err := p.closePositionCore(ctx, posID, false); err != nil {
		t.Logf("closePositionCore err (收尾校验可报): %v", err)
	}
	// spot 腿：BUY 开 → SELL 平，PositionSide 空（现货无 posSide）
	if spotExe.got.Side != "SELL" || spotExe.got.Qty != 10 {
		t.Errorf("spot close order=%+v want SELL qty=10", spotExe.got)
	}
	if spotExe.got.PositionSide != "" {
		t.Errorf("spot 腿 PositionSide 应空, got %q", spotExe.got.PositionSide)
	}
	// perp 腿：SELL 开 → BUY 平 + PositionSide=short → 执行器映射 reduceOnly=true（D-108）
	if perpExe.got.Side != "BUY" || perpExe.got.PositionSide != "short" || perpExe.got.Qty != 10 {
		t.Errorf("perp close order=%+v want BUY posSide=short qty=10", perpExe.got)
	}
}

// reduceOnly 落地断言：placeOKX perp 平仓 body 含 "reduceOnly":true（KB cex-close-reduceonly-missing 防回归）。
func TestD473_OKXPerpCloseReduceOnlyBody(t *testing.T) {
	var bodies []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v5/account/config":
			w.Write([]byte(`{"code":"0","data":[{"posMode":"long_short_mode"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v5/public/instruments"):
			w.Write([]byte(`{"code":"0","data":[{"ctVal":"1","lotSz":"1","state":"live"}]}`))
		case strings.HasPrefix(r.URL.Path, "/api/v5/trade/order"):
			if r.Method == http.MethodPost {
				b := make([]byte, r.ContentLength)
				r.Body.Read(b)
				bodies = append(bodies, string(b))
			}
			w.Write([]byte(`{"code":"0","data":[{"ordId":"c1","sCode":"0","avgPx":"0.5","accFillSz":"10"}]}`))
		default:
			w.Write([]byte(`{"code":"0","data":[]}`))
		}
	}))
	defer srv.Close()
	ex := NewCEXExecutor("okx", "perp", "k", "s", "p", srv.URL, false)
	_, err := ex.PlaceOrder(context.Background(), Order{Symbol: "SKL-USDT-SWAP", Side: "BUY", Qty: 10, PositionSide: "short"})
	if err != nil {
		t.Fatalf("place: %v", err)
	}
	if len(bodies) != 1 {
		t.Fatalf("bodies=%v", bodies)
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(bodies[0]), &m); err != nil {
		t.Fatalf("body json: %v", err)
	}
	if m["reduceOnly"] != true || m["posSide"] != "short" {
		t.Fatalf("perp 平仓 body=%s want reduceOnly=true posSide=short", bodies[0])
	}
}

var _ = time.Now // silence unused if helpers trimmed
