// D-524：okx 方言归一化 + instruments 目录闸测试。
package exchange

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestOKXSubCandidates(t *testing.T) {
	cases := []struct{ in, spot, perp string }{
		{"BTC", "BTC-USDT", "BTC-USDT-SWAP"},           // env bare base
		{"ATOMUSDT", "ATOM-USDT", "ATOM-USDT-SWAP"},    // canonical（watchlist divergence 写入形态）
		{"BZ_USDT", "BZ-USDT", "BZ-USDT-SWAP"},         // gate 原生形态
		{"XAU-USDT", "XAU-USDT", "XAU-USDT-SWAP"},      // okx spot 原生
		{"XAU-USDT-SWAP", "XAU-USDT", "XAU-USDT-SWAP"}, // okx perp 原生（spot 候选交目录裁决）
		{"xau-usdt-swap", "XAU-USDT", "XAU-USDT-SWAP"}, // 小写归一
		{"  ada  ", "ADA-USDT", "ADA-USDT-SWAP"},       // 空白修剪
		{"XAUUSD", "XAUUSD-USDT", "XAUUSD-USDT-SWAP"},  // USD 计价：候选照出、目录裁决为不存在
		{"牛来", "牛来-USDT", "牛来-USDT-SWAP"},              // 非 ASCII 同理交目录裁决
		{"USDT", "", ""}, // 空 base 防 ""→"-USDT" 裸 id
		{"", "", ""},
	}
	for _, c := range cases {
		sp, sw := okxSubCandidates(c.in)
		if sp != c.spot || sw != c.perp {
			t.Errorf("%q → (%q,%q), want (%q,%q)", c.in, sp, sw, c.spot, c.perp)
		}
	}
}

// instSrv 假 instruments 端点：SPOT/SWAP 各回一组（含非 live 条目验证过滤）。
func instSrv(t *testing.T, code string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if code != "0" {
			w.WriteHeader(500)
			return
		}
		var body string
		switch r.URL.Query().Get("instType") {
		case "SPOT":
			body = `{"code":"0","data":[{"instId":"BTC-USDT","state":"live"},{"instId":"PRE-USDT","state":"preopen"},{"instId":"XAUT-USDT","state":"live"}]}`
		case "SWAP":
			body = `{"code":"0","data":[{"instId":"BTC-USDT-SWAP","state":"live"},{"instId":"XAU-USDT-SWAP","state":"live"},{"instId":"SUS-USDT-SWAP","state":"suspend"}]}`
		default:
			t.Errorf("unexpected instType %q", r.URL.Query().Get("instType"))
		}
		fmt.Fprint(w, body)
	}))
}

func testCatalog(srv *httptest.Server) *okxInstCatalog {
	return &okxInstCatalog{client: srv.Client(), base: srv.URL}
}

func TestOKXInstCatalog_FetchAndPick(t *testing.T) {
	srv := instSrv(t, "0")
	defer srv.Close()
	c := testCatalog(srv)
	if c.loaded() {
		t.Fatal("catalog should not be loaded before ensure")
	}
	// 未加载：pick 透传候选（可用性优先回退）
	sp, sw := c.pick("GONE-USDT", "GONE-USDT-SWAP")
	if sp != "GONE-USDT" || sw != "GONE-USDT-SWAP" {
		t.Fatalf("unloaded pick should passthrough, got (%q,%q)", sp, sw)
	}
	c.ensure(context.Background())
	if !c.loaded() {
		t.Fatal("catalog not loaded after ensure")
	}
	sp, sw = c.pick("BTC-USDT", "BTC-USDT-SWAP")
	if sp != "BTC-USDT" || sw != "BTC-USDT-SWAP" {
		t.Fatalf("BTC candidates dropped: (%q,%q)", sp, sw)
	}
	// state!=live 过滤：preopen spot + suspend swap 均判不存在
	sp, sw = c.pick("PRE-USDT", "SUS-USDT-SWAP")
	if sp != "" || sw != "" {
		t.Fatalf("non-live should be filtered, got (%q,%q)", sp, sw)
	}
	// 不存在符号整体剔除
	sp, sw = c.pick("牛来-USDT", "牛来-USDT-SWAP")
	if sp != "" || sw != "" {
		t.Fatalf("nonexistent should be filtered, got (%q,%q)", sp, sw)
	}
	// 部分存活：spot 不存在 perp 存在（xasset perp 形态）
	sp, sw = c.pick("XAU-USDT", "XAU-USDT-SWAP")
	if sp != "" || sw != "XAU-USDT-SWAP" {
		t.Fatalf("partial pick wrong: (%q,%q)", sp, sw)
	}
}

func TestOKXInstCatalog_FailureKeepsOldSet(t *testing.T) {
	srv := instSrv(t, "0")
	c := testCatalog(srv)
	c.ensure(context.Background())
	if !c.loaded() {
		t.Fatal("precondition: catalog loaded")
	}
	srv.Close()                       // 服务端下线
	c.at = time.Now().Add(-time.Hour) // 强制过期再拉
	c.ensure(context.Background())    // 拉取失败
	if sp, _ := c.pick("BTC-USDT", ""); sp != "BTC-USDT" {
		t.Fatal("failed refresh must keep last good set")
	}
}

func TestOKXSubscribeArgs_Filtered(t *testing.T) {
	srv := instSrv(t, "0")
	defer srv.Close()
	o := &OKXWebSocket{catalog: testCatalog(srv)}
	subscribed, quarantined := map[string]bool{}, map[string]bool{}
	args := o.subscribeArgs(context.Background(),
		[]string{"BTC", "XAU-USDT-SWAP", "ATOMUSDT", "牛来"}, subscribed, quarantined)
	// BTC→2+1 funding 共3 arg；XAU-USDT-SWAP→perp 2 arg（spot 目录无）；
	// ATOMUSDT→目录无=隔离零 arg；牛来→零 arg。合计 5。
	if len(args) != 5 {
		t.Fatalf("args len=%d want 5: %v", len(args), args)
	}
	if !quarantined["ATOMUSDT"] || !quarantined["牛来"] {
		t.Fatalf("quarantine missing: %v", quarantined)
	}
	// 差集语义：重复调用不再重发已订阅
	if args2 := o.subscribeArgs(context.Background(), []string{"BTC"}, subscribed, quarantined); len(args2) != 0 {
		t.Fatalf("resubscribe should emit nothing, got %v", args2)
	}
}
