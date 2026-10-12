// d537_gate_pairs_test.go —— D-537 gate 合法 pair 白名单红绿锚。
// 病灶：spot.tickers 对 payload 任一非法 pair 静默拒整批（探针实证零数据帧）。
// 钉死：①白名单加载后订阅 payload 仅含合法对 ②REST 失败回退不过滤（全集）
// ③OnAliveTick 差集同过滤（非法新 pair 不发帧）。
package exchange

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"nox/internal/data/bus"
)

// d537DirREST 伪 gate REST 目录：spot=currency_pairs[].id；futures=contracts[].name。
func d537DirREST(t *testing.T, spotIDs, futuresNames []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v4/spot/currency_pairs", func(w http.ResponseWriter, _ *http.Request) {
		var arr []map[string]string
		for _, id := range spotIDs {
			arr = append(arr, map[string]string{"id": id})
		}
		_ = json.NewEncoder(w).Encode(arr)
	})
	mux.HandleFunc("/api/v4/futures/usdt/contracts", func(w http.ResponseWriter, _ *http.Request) {
		var arr []map[string]string
		for _, n := range futuresNames {
			arr = append(arr, map[string]string{"name": n})
		}
		_ = json.NewEncoder(w).Encode(arr)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// d537SubPayload 从捕获的订阅帧解码 payload 数组。
func d537SubPayload(t *testing.T, frame []byte) []string {
	t.Helper()
	var m struct {
		Payload []string `json:"payload"`
	}
	if err := json.Unmarshal(frame, &m); err != nil {
		t.Fatalf("订阅帧非 JSON: %v", err)
	}
	return m.Payload
}

func d537GateWSTestable(cfg Config) *GateWebSocket {
	g := NewGateWebSocket(cfg)
	g.readIdle = 800 * time.Millisecond
	g.pingEvery = 100 * time.Millisecond
	g.reconnectBackoff = 50 * time.Millisecond
	return g
}

// TestD537_SubPayloadOnlyWhitelisted 白名单加载后，首连订阅 payload 仅含合法对。
func TestD537_SubPayloadOnlyWhitelisted(t *testing.T) {
	rest := d537DirREST(t, []string{"BTC_USDT", "ETH_USDT"}, []string{"BTC_USDT"})
	cfg := Config{
		Symbols:     []string{"BTC", "COPPER", "牛来"},
		GateBaseURL: rest.URL,
		Client:      rest.Client(),
	}
	g := d537GateWSTestable(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.spotWL.refresh(ctx)

	frames := make(chan []byte, 8)
	wsURL, _ := wsRunnerServer(t, false, frames)
	go g.runLoop(ctx, []string{wsURL}, []string{"spot.tickers"}, gateSpotWSURL, g.spotWL, g.handleSpot, func(bus.Quote) {}, nil)

	select {
	case data := <-frames:
		got := d537SubPayload(t, data)
		if len(got) != 1 || got[0] != "BTC_USDT" {
			t.Fatalf("订阅 payload 应只含 BTC_USDT: %v（COPPER_USDT/牛来_USDT 应被滤）", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("订阅帧未到")
	}
}

// TestD537_RestFailFallbackUnfiltered 目录 REST 失败（死端点）→ 从未成功=不过滤，
// payload=全集（回退现状不放大故障面）。
func TestD537_RestFailFallbackUnfiltered(t *testing.T) {
	cfg := Config{Symbols: []string{"BTC", "COPPER"}, GateBaseURL: "http://127.0.0.1:1", Client: &http.Client{Timeout: 300 * time.Millisecond}}
	g := d537GateWSTestable(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.spotWL.refresh(ctx) // 拉取失败→set 未载

	frames := make(chan []byte, 8)
	wsURL, _ := wsRunnerServer(t, false, frames)
	go g.runLoop(ctx, []string{wsURL}, []string{"spot.tickers"}, gateSpotWSURL, g.spotWL, g.handleSpot, func(bus.Quote) {}, nil)

	select {
	case data := <-frames:
		got := d537SubPayload(t, data)
		if len(got) != 2 {
			t.Fatalf("回退应全集不过滤: %v want [BTC_USDT COPPER_USDT]", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("订阅帧未到")
	}
}

// d537Provider 可变动态符号集（OnAliveTick 差集补订路径驱动）。
type d537Provider struct{ set *atomic.Value } // []string

func (p d537Provider) Symbols(string) []string {
	if v := p.set.Load(); v != nil {
		return v.([]string)
	}
	return nil
}

// TestD537_DiffResubFiltered OnAliveTick 差集补订同过白名单——新符号含非法对时
// 只发合法对帧；纯非法新增零帧。
func TestD537_DiffResubFiltered(t *testing.T) {
	rest := d537DirREST(t, []string{"BTC_USDT", "ETH_USDT"}, nil)
	prov := &atomic.Value{}
	cfg := Config{
		Symbols:     []string{"BTC"},
		SymProvider: d537Provider{set: prov},
		GateBaseURL: rest.URL,
		Client:      rest.Client(),
	}
	g := d537GateWSTestable(cfg)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	g.spotWL.refresh(ctx)

	frames := make(chan []byte, 16)
	wsURL, _ := wsRunnerServer(t, false, frames)
	go g.runLoop(ctx, []string{wsURL}, []string{"spot.tickers"}, gateSpotWSURL, g.spotWL, g.handleSpot, func(bus.Quote) {}, nil)

	// 首帧=[BTC_USDT]
	select {
	case data := <-frames:
		if got := d537SubPayload(t, data); len(got) != 1 || got[0] != "BTC_USDT" {
			t.Fatalf("首订阅 payload 错: %v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("首订阅帧未到")
	}

	// 新增 ETH（合法）+ COPPER（非法）→ 差集帧应只含 ETH_USDT
	prov.Store([]string{"ETH", "COPPER"})
	var sawDiff bool
	deadline := time.After(3 * time.Second)
	for !sawDiff {
		select {
		case data := <-frames:
			got := d537SubPayload(t, data)
			for _, p := range got {
				if p == "COPPER_USDT" {
					t.Fatalf("非法 pair 进入差集帧: %v", got)
				}
			}
			for _, p := range got {
				if p == "ETH_USDT" {
					sawDiff = true
				}
			}
		case <-deadline:
			t.Fatal("合法新 pair ETH_USDT 差集补订帧未到")
		}
	}

	// 纯非法新增 → 观察窗内不再出现任何订阅帧
	prov.Store([]string{"GARBAGE"})
	select {
	case data := <-frames:
		if got := d537SubPayload(t, data); len(got) != 0 {
			t.Fatalf("纯非法新增应零差集帧: %v", got)
		}
	case <-time.After(400 * time.Millisecond):
	}
}
