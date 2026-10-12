// d155_pulse_test.go —— D-155 方案A：okx 脉冲订阅面+帧分发锚测。
package exchange

import (
	"context"
	"testing"
	"time"

	"arb/internal/data/bus"
)

// TestD155_OkxPulseSubscription 订阅面：SWAP 加 mark-price、spot 加 index-tickers。
func TestD155_OkxPulseSubscription(t *testing.T) {
	o := NewOKXWebSocket(Config{Symbols: []string{"XAU"}})
	// catalog 注入存活集（否则 ensure 走真实 HTTP 拉取）
	o.catalog = &okxInstCatalog{set: map[string]bool{"XAU-USDT": true, "XAU-USDT-SWAP": true}, at: time.Now()}
	ctx := context.Background()
	args := o.subscribeArgs(ctx, o.syms(), map[string]bool{}, map[string]bool{})
	var tickersPerp, funding, markPrice, indexTick bool
	for _, a := range args {
		switch a["channel"] {
		case "tickers":
			if a["instId"] == "XAU-USDT-SWAP" {
				tickersPerp = true
			}
		case "funding-rate":
			funding = true
		case "mark-price":
			markPrice = a["instId"] == "XAU-USDT-SWAP"
		case "index-tickers":
			indexTick = a["instId"] == "XAU-USDT"
		}
	}
	if !tickersPerp || !funding || !markPrice || !indexTick {
		t.Fatalf("脉冲频道缺订: tickers=%v funding=%v mark=%v index=%v args=%v",
			tickersPerp, funding, markPrice, indexTick, args)
	}
}

// TestD155_OkxPulseFrame 脉冲帧分发：Touch 键形正确、非业务帧（return false）。
func TestD155_OkxPulseFrame(t *testing.T) {
	o := NewOKXWebSocket(Config{})
	type hit struct{ sym, vk string }
	var hits []hit
	o.pulseFn = func(sym, vk string) { hits = append(hits, hit{sym, vk}) }
	var pubs int
	publish := func(bus.Quote) { pubs++ }
	// mark-price → okx:perp
	if o.handleMessage(context.Background(), []byte(
		`{"arg":{"channel":"mark-price","instId":"XAU-USDT-SWAP"},"data":[{"instId":"XAU-USDT-SWAP","markPx":"4000.5","ts":"1700000000000"}]}`),
		publish) {
		t.Fatal("mark-price 脉冲帧被记业务帧")
	}
	// index-tickers → okx:spot
	if o.handleMessage(context.Background(), []byte(
		`{"arg":{"channel":"index-tickers","instId":"XAU-USDT"},"data":[{"instId":"XAU-USDT","idxPx":"4001.0","ts":"1700000000000"}]}`),
		publish) {
		t.Fatal("index-tickers 脉冲帧被记业务帧")
	}
	if len(hits) != 2 {
		t.Fatalf("脉冲打点=%d want 2", len(hits))
	}
	if hits[0].sym != "XAUUSDT" || hits[0].vk != "okx:perp" {
		t.Fatalf("mark-price 脉冲键错: %+v", hits[0])
	}
	if hits[1].sym != "XAUUSDT" || hits[1].vk != "okx:spot" {
		t.Fatalf("index-tickers 脉冲键错: %+v", hits[1])
	}
	// 脉冲不写报价值：publish 零次
	if pubs != 0 {
		t.Fatalf("脉冲帧触发 publish: %d", pubs)
	}
}
