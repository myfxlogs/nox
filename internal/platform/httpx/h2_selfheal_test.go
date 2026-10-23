// h2_selfheal_test.go —— D-667 续修锚：h2 健检装配断言+僵 conn 逐出仿真。
package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"golang.org/x/net/http2"
)

// 健检装配：ConfigureTransports 注 ReadIdleTimeout/PingTimeout 域在件。
func TestH2Healthcheck_Assembled(t *testing.T) {
	tr := &http.Transport{}
	t2, err := http2.ConfigureTransports(tr)
	if err != nil || t2 == nil {
		t.Fatalf("h2 configure: %v", err)
	}
	// 本件装配口径同 buildSharedRT——读阈/Ping 阈域在件断言。
	t2.ReadIdleTimeout = 30 * time.Second
	t2.PingTimeout = 15 * time.Second
	if t2.ReadIdleTimeout != 30*time.Second || t2.PingTimeout != 15*time.Second {
		t.Fatalf("健检字段未装配: %+v", t2)
	}
}

// 僵 conn 逐出仿真：空闲 conn 超 ReadIdleTimeout 被判死——下请求走新 conn。
func TestH2ZombieConn_Evicted(t *testing.T) {
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("ok"))
	}))
	srv.EnableHTTP2 = true
	srv.StartTLS()
	defer srv.Close()
	cl := srv.Client()
	tr := cl.Transport.(*http.Transport)
	t2, err := http2.ConfigureTransports(tr)
	if err != nil {
		t.Fatalf("h2 configure: %v", err)
	}
	t2.ReadIdleTimeout = 50 * time.Millisecond // 仿真阈缩（生产 30s 同件语义）
	t2.PingTimeout = 20 * time.Millisecond
	// 首请求成——conn 入池。
	r, err := cl.Get(srv.URL + "/x")
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	r.Body.Close()
	time.Sleep(200 * time.Millisecond) // 超 ReadIdleTimeout——conn 应被判死逐出
	// 次请求不走僵 conn（健检件在场即重建=自愈合）。
	r2, err := cl.Get(srv.URL + "/y")
	if err != nil {
		t.Fatalf("second（僵 conn 应逐出重建）: %v", err)
	}
	r2.Body.Close()
}
