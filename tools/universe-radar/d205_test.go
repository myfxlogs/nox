// d205_test.go —— D-205 锚：空 proxy→per-source 直连 base 拆分/okx funding 不依赖
// 订阅集可评全量 SWAP/binance premiumIndex 全量面/文案「行在值空 vs 无行」对拍。
package main

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// ①空 proxy→直连 base：三源各归官方公共域；非空→proxy 现状径。
func TestD205_DirectBaseSplit(t *testing.T) {
	srcs := allSources(http.DefaultClient, "", nil, t.TempDir())
	want := map[string]string{
		"binance": "https://fapi.binance.com",
		"okx":     "https://www.okx.com",
		"gate":    "https://api.gateio.ws",
	}
	for _, s := range srcs {
		var base string
		switch v := s.(type) {
		case binance:
			base = v.base
		case okx:
			base = v.base
		case gate:
			base = v.base
		}
		if base != want[s.Venue()] {
			t.Fatalf("%s base=%q 应直连 %s", s.Venue(), base, want[s.Venue()])
		}
	}
	srcs = allSources(http.DefaultClient, "http://proxy:9", nil, t.TempDir())
	if b := srcs[0].(binance); b.base != "http://proxy:9" {
		t.Fatalf("proxy 非空应保 cex-proxy 径: %s", b.base)
	}
}

// ②binance funding 公共 REST 全量面：premiumIndex+fundingInfo 两调用评全量，
// 零订阅依赖（pool=nil 禁 facts 兜底）。
func TestD205_BinanceFundingREST(t *testing.T) {
	var hits []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path)
		switch {
		case strings.HasSuffix(r.URL.Path, "exchangeInfo"):
			fmt.Fprint(w, `{"symbols":[{"symbol":"BTCUSDT","baseAsset":"BTC","quoteAsset":"USDT","contractType":"PERPETUAL","underlyingType":"COIN","status":"TRADING","onboardDate":1}]}`)
		case strings.HasSuffix(r.URL.Path, "premiumIndex"):
			fmt.Fprint(w, `[{"symbol":"BTCUSDT","lastFundingRate":"0.0001"},{"symbol":"ETHUSDT","lastFundingRate":"-0.0002"}]`)
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
	if len(rows) != 1 {
		t.Fatalf("rows=%d", len(rows))
	}
	if rows[0].FundingRate != 0.0001 || rows[0].IntervalH != 8 {
		t.Fatalf("REST funding 未中：%+v", rows[0])
	}
	// ETHUSDT 在 premiumIndex 面但不在枚举集——不扩集（REST 面≠universe）。
	for _, r := range rows {
		if r.Symbol == "ETHUSDT" {
			t.Fatal("REST 面不得扩 universe 枚举集")
		}
	}
}

// ②okx funding 逐 instId 限频循环：全量 SWAP 可评不依赖订阅集（pool=nil）。
func TestD205_OKXFundingREST(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "instruments"):
			fmt.Fprint(w, `{"data":[{"instId":"AAA-USDT-SWAP","state":"live","listTime":"1","ctVal":"1"},{"instId":"BBB-USDT-SWAP","state":"live","listTime":"1","ctVal":"1"}]}`)
		case strings.Contains(r.URL.Path, "tickers"):
			fmt.Fprint(w, `{"data":[{"instId":"AAA-USDT-SWAP","volCcy24h":"1000","last":"2"}]}`)
		case strings.Contains(r.URL.Path, "funding-rate"):
			inst := r.URL.Query().Get("instId")
			if inst == "AAA-USDT-SWAP" {
				fmt.Fprint(w, `{"data":[{"fundingRate":"0.0005","fundingTime":"1000000","nextFundingTime":"29800000"}]}`)
			} else {
				fmt.Fprint(w, `{"data":[]}`) // BBB 无行——行在值空
			}
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
		t.Fatalf("全量 SWAP 枚举缺：%d", len(rows))
	}
	var aaa, bbb *Row
	for i := range rows {
		if rows[i].Symbol == "AAA-USDT-SWAP" {
			aaa = &rows[i]
		}
		if rows[i].Symbol == "BBB-USDT-SWAP" {
			bbb = &rows[i]
		}
	}
	if aaa.FundingRate != 0.0005 || aaa.IntervalH != 8 {
		t.Fatalf("okx funding 未中：%+v", aaa)
	}
	if bbb.FundingRate != 0 || bbb.IntervalH != 0 {
		t.Fatalf("无行应诚实零值：%+v", bbb)
	}
}

// ③文案对拍：「行在值空」vs「无行」分态。
func TestD205_FundNoteText(t *testing.T) {
	var buf bytes.Buffer
	log.SetOutput(&buf)
	defer log.SetOutput(os.Stderr)
	fundNote("okx", 0, 0)
	fundNote("okx", 10, 7)
	out := buf.String()
	if !strings.Contains(out, "无行（venue 枚举空集）") {
		t.Fatalf("无行文案缺：%s", out)
	}
	if !strings.Contains(out, "行在值空：覆盖 7/10") {
		t.Fatalf("行在值空文案缺：%s", out)
	}
}
