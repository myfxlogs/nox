// d578_depth_test.go —— D-578 锚：三通道首采/失败诚实/节拍对齐/指标正确性/env 开关。
package cexscan

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// bookBody binance/gate 真形（字符串对数组）。
func bookBody(bidP, bidQ, askP, askQ string) string {
	return fmt.Sprintf(`{"bids":[["%s","%s"],["%s","1"]],"asks":[["%s","%s"],["%s","1"]]}`,
		bidP, bidQ, bidP[:len(bidP)-1]+"8", askP, askQ, askP[:len(askP)-1]+"2")
}

func newDepthFake(t *testing.T, okxCode string) *depthSampler {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/api/v3/depth"):
			fmt.Fprint(w, bookBody("3000", "100", "3001", "140"))
		case strings.Contains(r.URL.Path, "/order_book"):
			fmt.Fprint(w, bookBody("3000", "140", "3001", "140"))
		case strings.Contains(r.URL.Path, "/market/books"):
			if okxCode != "0" {
				fmt.Fprintf(w, `{"code":"%s","msg":"Instrument ID does not exist","data":[]}`, okxCode)
				return
			}
			fmt.Fprint(w, `{"code":"0","msg":"","data":[`+bookBody("3000", "190", "3001", "300")+`]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	oldB, oldG := binanceDepthBase, gateDepthBase
	binanceDepthBase, gateDepthBase = srv.URL, srv.URL
	t.Cleanup(func() { binanceDepthBase, gateDepthBase = oldB, oldG })
	d := newDepthSampler(nil, srv.URL)
	d.client = srv.Client()
	return d
}

// A1+A5：三通道 fake 盘口 → 各对三行 facts（venue:spot 键域+canonical symbol+三 kind），
// 深度/点差逐值断言（bid 3000×100=300000，0.5% 带内二档 2999.98 计入）。
func TestD578_A1_DepthSampleOnce(t *testing.T) {
	d := newDepthFake(t, "0")
	// fetchBook 三通道各返（真形）。
	for _, venue := range []string{"binance", "gate", "okx"} {
		native := depthVenueSyms["XAUT"][venue]
		bids, asks, err := d.fetchBook(context.Background(), venue, native)
		if err != nil || len(bids) != 2 || len(asks) != 2 {
			t.Fatalf("%s fetchBook err=%v bids=%v asks=%v", venue, err, bids, asks)
		}
	}
	// A5 指标正确性：mid=3000.5，带界=[2985.4975, 3015.5025]——2985.5 恰带内/2985.4 带外，
	// 3015.5 恰带内/3015.51 带外（两侧边界挂单对拍）。
	bids := [][2]float64{{3000, 100}, {2985.5, 1}, {2985.4, 1000}}
	asks := [][2]float64{{3001, 140}, {3015.5, 1}, {3015.51, 1000}} // 3015.51>3015.5025 出带
	fs := depthFacts("binance", "XAUT", bids, asks, time.Unix(1, 0))
	if len(fs) != 3 {
		t.Fatalf("facts n=%d want 3", len(fs))
	}
	var spread, bidUSD, askUSD float64
	for _, f := range fs {
		if f.Venue != "binance:spot" || f.Symbol != "XAUT" || f.Src != depthSrc {
			t.Fatalf("fact meta wrong: %+v", f)
		}
		switch f.Kind {
		case depthSpreadKind:
			spread = f.Value
		case depthBidKind:
			bidUSD = f.Value
		case depthAskKind:
			askUSD = f.Value
		}
	}
	wantSpread := (3001.0 - 3000.0) / 3000.5 * 1e4
	if d := spread - wantSpread; d > 1e-9 || d < -1e-9 {
		t.Fatalf("spread=%v want %v", spread, wantSpread)
	}
	if bidUSD != 3000*100+2985.5*1 || askUSD != 3001*140+3015.5*1 {
		t.Fatalf("depth bid=%v ask=%v", bidUSD, askUSD)
	}
}

// A2：okx 51001 → 该对零行 warn；binance/gate 正常（经 sampleOnce 落库行数断言）。
func TestD578_A2_OKXMissingPair(t *testing.T) {
	d := newDepthFake(t, "51001")
	_, _, err := d.fetchBook(context.Background(), "okx", "XAUT-USDT")
	if err == nil || !strings.Contains(err.Error(), "51001") {
		t.Fatalf("okx 51001 应返错: %v", err)
	}
	// 其余通道不受 okx 失败影响。
	b, _, err := d.fetchBook(context.Background(), "binance", "XAUTUSDT")
	if err != nil || len(b) == 0 {
		t.Fatalf("binance should still work: %v", err)
	}
}

// A3：nextDepthTick ∈ {04,10,16,22} 北京时刻；03:59→04:00、22:01→次日04:00 边界断言。
func TestD578_A3_TickAlign(t *testing.T) {
	cases := []struct {
		in   time.Time
		want int
	}{
		{time.Date(2026, 9, 30, 3, 59, 0, 0, bjDepth), 4},
		{time.Date(2026, 9, 30, 4, 0, 0, 0, bjDepth), 10},
		{time.Date(2026, 9, 30, 15, 30, 0, 0, bjDepth), 16},
		{time.Date(2026, 9, 30, 22, 1, 0, 0, bjDepth), 4}, // 次日
		{time.Date(2026, 9, 30, 0, 0, 0, 0, bjDepth), 4},
	}
	for _, c := range cases {
		got := nextDepthTick(c.in)
		if got.In(bjDepth).Hour() != c.want || got.Minute() != 0 {
			t.Fatalf("nextDepthTick(%v)=%v want hour %d", c.in, got, c.want)
		}
		if !got.After(c.in) {
			t.Fatalf("tick must be future: %v <= %v", got, c.in)
		}
	}
}

// A6：ARB_DEPTH_SAMPLER_OFF=1 → Run 立即返回不采。
func TestD578_A6_EnvOff(t *testing.T) {
	t.Setenv("ARB_DEPTH_SAMPLER_OFF", "1")
	d := newDepthFake(t, "0")
	done := make(chan struct{})
	go func() { d.Run(context.Background()); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OFF=1 应立即返回")
	}
}
