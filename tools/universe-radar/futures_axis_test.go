package main

// 宇宙雷达仪器轴扩列锚测：交割合约枚举/后缀剥/basis_ann/首扫静默/事件径。
// 交割=funding 缺≠剔除，走到期收敛基差轴；expiry/行情缺 fail-closed nil。

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func TestFutures_NormBase(t *testing.T) {
	for _, tc := range []struct{ sym, want string }{
		{"BTC-USDT-241227", "BTC"},   // okx 六位数交割
		{"BTCUSDT_240927", "BTC"},    // binance 下划线季度
		{"BTC_USDT_20241227", "BTC"}, // gate 八位数交割
		{"BTC-USDT-SWAP", "BTC"},     // perp 回归
	} {
		if got := normBase(tc.sym); got != tc.want {
			t.Fatalf("normBase(%q)=%q want %q", tc.sym, got, tc.want)
		}
	}
}

func TestFutures_BasisAnn(t *testing.T) {
	now := time.Now()
	exp := now.Add(90 * 24 * time.Hour)
	got := basisAnn(105, 100, exp, now)
	if got == nil {
		t.Fatal("basis_ann 应有值")
	}
	want := 0.05 * 365 / 90 * 100
	if d := *got - want; d > 0.2 || d < -0.2 {
		t.Fatalf("basis_ann=%v want≈%.2f", *got, want)
	}
	if basisAnn(0, 100, exp, now) != nil || basisAnn(105, 0, exp, now) != nil {
		t.Fatal("mark/index 缺应 nil")
	}
	if basisAnn(105, 100, time.Time{}, now) != nil {
		t.Fatal("expiry 缺应 nil（禁猜到期日）")
	}
	if basisAnn(105, 100, now.Add(-24*time.Hour), now) != nil {
		t.Fatal("已过期应 nil")
	}
	// perp 轴回归：funding 年化公式不动（0.0001×3/日×365×100=10.95%）
	a := annualize(0.0001, 8)
	if a == nil || *a < 10.9 || *a > 11.0 {
		t.Fatalf("perp annualize 回归破：%v", a)
	}
}

// binance 交割枚举：CURRENT_QUARTER 收+deliveryDate→ExpiryTS；funding 缺行不剔。
func TestFutures_BinanceQuarter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "exchangeInfo"):
			fmt.Fprint(w, `{"symbols":[
				{"symbol":"BTCUSDT","baseAsset":"BTC","quoteAsset":"USDT","contractType":"PERPETUAL","underlyingType":"COIN","status":"TRADING","onboardDate":1},
				{"symbol":"BTCUSDT_241227","baseAsset":"BTC","quoteAsset":"USDT","contractType":"CURRENT_QUARTER","underlyingType":"COIN","status":"TRADING","deliveryDate":1768000000000},
				{"symbol":"ETHUSDT_NQ","baseAsset":"ETH","quoteAsset":"USDT","contractType":"NEXT_QUARTER","underlyingType":"COIN","status":"TRADING","deliveryDate":1772000000000},
				{"symbol":"BTCBUSD","baseAsset":"BTC","quoteAsset":"BUSD","contractType":"PERPETUAL","status":"TRADING"}]}`)
		case strings.HasSuffix(r.URL.Path, "premiumIndex"):
			fmt.Fprint(w, `[{"symbol":"BTCUSDT","lastFundingRate":"0.0001"}]`)
		case strings.HasSuffix(r.URL.Path, "fundingInfo"):
			fmt.Fprint(w, `[{"symbol":"BTCUSDT","fundingIntervalHours":8}]`)
		default:
			fmt.Fprint(w, `{}`)
		}
	}))
	defer srv.Close()
	b := binance{hc: srv.Client(), base: srv.URL, pool: nil, cacheDir: t.TempDir()}
	rows, err := b.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 {
		t.Fatalf("rows=%d want 3（perp+两季交，BUSD 报价剔）", len(rows))
	}
	var perp, q *Row
	for i := range rows {
		switch rows[i].Symbol {
		case "BTCUSDT":
			perp = &rows[i]
		case "BTCUSDT_241227":
			q = &rows[i]
		}
	}
	if perp.ContractType != ctPerp || perp.FundingRate != 0.0001 {
		t.Fatalf("perp 回归破：%+v", perp)
	}
	if q.ContractType != ctFutures || q.ExpiryTS.IsZero() || q.ExpiryTS.Unix() != 1768000000 {
		t.Fatalf("交割行破：%+v", q)
	}
	if q.FundingRate != 0 || q.IntervalH != 0 {
		t.Fatalf("交割 funding 应零值不剔：%+v", q)
	}
}

// okx FUTURES 枚举+expiry+index 轴+funding 端点不打交割。
func TestFutures_Okx(t *testing.T) {
	var fundingHits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "instruments"):
			if r.URL.Query().Get("instType") == "FUTURES" {
				fmt.Fprint(w, `{"data":[{"instId":"BTC-USDT-241227","instType":"FUTURES","state":"live","listTime":"1","ctVal":"0.01","expTime":"1735000000000"},
					{"instId":"BTC-USD-241227","instType":"FUTURES","state":"live","listTime":"1","ctVal":"0.01","expTime":"1735000000000"}]}`)
			} else {
				fmt.Fprint(w, `{"data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","state":"live","listTime":"1","ctVal":"0.01"}]}`)
			}
		case strings.Contains(r.URL.Path, "index-tickers"):
			fmt.Fprint(w, `{"data":[{"idxPx":"100000"}]}`)
		case strings.Contains(r.URL.Path, "tickers") && r.URL.Query().Get("instType") == "FUTURES":
			fmt.Fprint(w, `{"data":[{"instId":"BTC-USDT-241227","volCcy24h":"1000","last":"101000"}]}`)
		case strings.Contains(r.URL.Path, "tickers"):
			fmt.Fprint(w, `{"data":[{"instId":"BTC-USDT-SWAP","volCcy24h":"1000","last":"100000"}]}`)
		case strings.Contains(r.URL.Path, "funding-rate"):
			fundingHits = append(fundingHits, r.URL.Query().Get("instId"))
			fmt.Fprint(w, `{"data":[{"fundingRate":"0.0005","fundingTime":"1000000","nextFundingTime":"29800000"}]}`)
		default:
			fmt.Fprint(w, `{"data":[]}`)
		}
	}))
	defer srv.Close()
	o := okx{hc: srv.Client(), base: srv.URL, pool: nil, cacheDir: t.TempDir(), throttle: 0}
	rows, err := o.Scan(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("rows=%d want 2（USD 本位交割剔）", len(rows))
	}
	var fut *Row
	for i := range rows {
		if rows[i].Symbol == "BTC-USDT-241227" {
			fut = &rows[i]
		}
	}
	if fut == nil || fut.ContractType != ctFutures || fut.Base != "BTC" {
		t.Fatalf("交割行破：%+v", fut)
	}
	if fut.ExpiryTS.IsZero() || fut.ExpiryTS.Unix() != 1735000000 {
		t.Fatalf("expiry 破：%+v", fut.ExpiryTS)
	}
	if fut.Mark != 101000 || fut.Index != 100000 {
		t.Fatalf("mark/index 破：%+v", fut)
	}
	for _, h := range fundingHits {
		if strings.HasSuffix(h, "-241227") {
			t.Fatalf("交割符号不得打 funding-rate：%s", h)
		}
	}
}

// gate 交割枚举：expire_time>0 收行。
func TestFutures_GateDelivery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"name":"BTC_USDT","expire_time":0,"funding_rate":"0.0001","funding_interval":28800},
			{"name":"BTC_USDT_20241227","expire_time":1735257600,"funding_rate":"0","funding_interval":0}]`)
	}))
	defer srv.Close()
	g := gate{hc: srv.Client(), base: srv.URL}
	rows, err := g.futuresContracts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Symbol != "BTC_USDT_20241227" || rows[0].ContractType != ctFutures {
		t.Fatalf("交割枚举破：%+v", rows)
	}
	if rows[0].ExpiryTS.Unix() != 1735257600 || rows[0].Base != "BTC" {
		t.Fatalf("expiry/base 破：%+v", rows[0])
	}
}

// 首扫静默：旧 universe 无 FUTURES 行→交割新行零事件；次轴已立→NEW/DELISTED 照常。
func TestFutures_BootstrapSilent(t *testing.T) {
	old := map[string]pgstore.CexUniverseRow{
		"okx|BTC-USDT-SWAP": {Venue: "okx", Symbol: "BTC-USDT-SWAP", Status: "TRADING"},
	}
	rows := normalize("okx", []Row{
		{Symbol: "BTC-USDT-SWAP", ContractType: ctPerp, Status: "TRADING"},
		{Symbol: "BTC-USDT-241227", ContractType: ctFutures, Status: "TRADING", Base: "BTC"},
	}, nil, nil)
	if evs := diffUniverse(old, rows); len(evs) != 0 {
		t.Fatalf("交割轴首扫应零事件：%+v", evs)
	}
	// 轴已立：新交割上市→NEW；旧交割状态转移→DELISTED
	old["okx|BTC-USDT-241227"] = pgstore.CexUniverseRow{Venue: "okx", Symbol: "BTC-USDT-241227", Status: "TRADING", ContractType: ctFutures}
	rows = normalize("okx", []Row{
		{Symbol: "BTC-USDT-SWAP", ContractType: ctPerp, Status: "TRADING"},
		{Symbol: "BTC-USDT-241227", ContractType: ctFutures, Status: "TRADING", Base: "BTC"},
		{Symbol: "ETH-USDT-241227", ContractType: ctFutures, Status: "TRADING", Base: "ETH"},
		{Symbol: "SOL-USDT-241227", ContractType: ctFutures, Status: "suspended", Base: "SOL"},
	}, nil, nil)
	old["okx|SOL-USDT-241227"] = pgstore.CexUniverseRow{Venue: "okx", Symbol: "SOL-USDT-241227", Status: "TRADING", ContractType: ctFutures}
	evs := diffUniverse(old, rows)
	var newCnt, delCnt int
	for _, e := range evs {
		if e.EventType == "NEW" && e.Symbol == "ETH-USDT-241227" {
			newCnt++
		}
		if e.EventType == "DELISTED" && e.Symbol == "SOL-USDT-241227" {
			delCnt++
		}
	}
	if newCnt != 1 || delCnt != 1 {
		t.Fatalf("NEW/DELISTED 径破：%+v", evs)
	}
}
