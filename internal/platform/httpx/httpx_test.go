package httpx

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 主端传输错误（拒连）→ 同请求改发备用端点成功；body 与 token 头重放。
func TestFailoverRT_PrimaryDownFallsBack(t *testing.T) {
	var gotTok string
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTok = r.Header.Get("X-Proxy-Token")
		_, _ = w.Write([]byte("via-backup"))
	}))
	defer back.Close()
	Configure("", "tok-fb", [][2]string{{"http://127.0.0.1:1", back.URL}}) // 拒连=传输错
	defer Configure("", "", nil)

	resp, err := Client().Get("http://127.0.0.1:1/fapi/v1/x")
	if err != nil {
		t.Fatalf("failover 应成功: %v", err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if string(data) != "via-backup" {
		t.Fatalf("resp=%q", data)
	}
	if gotTok != "tok-fb" {
		t.Fatalf("备份重试未带 token: %q", gotTok)
	}
}

// A3（D3 方法门控）：POST/PUT/DELETE/PATCH 传输错不回退（备端零命中）——
// bodyless-POST 同签名重放=二次下单窗口（风控优先裁定）；GET 仍回退（上测已钉）。
func TestFailoverRT_NonIdempotentNoFallback(t *testing.T) {
	var backHits int
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backHits++
	}))
	defer back.Close()
	Configure("", "tok-fb", [][2]string{{"http://127.0.0.1:1", back.URL}})
	defer Configure("", "", nil)

	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodPatch} {
		req, _ := http.NewRequest(m, "http://127.0.0.1:1/fapi/v1/order", nil)
		if _, err := Client().Do(req); err == nil {
			t.Fatalf("%s 主端拒连应上抛传输错", m)
		}
	}
	if backHits != 0 {
		t.Fatalf("非幂等方法不应回退备端: %d hits", backHits)
	}
}

// A1（D1）：回退成功且**原请求 URL 不被改写**（Clone 浅拷共享 URL 指针=契约违反）。
func TestFailoverRT_OriginalURLUnchanged(t *testing.T) {
	var backHost string
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backHost = r.Host
	}))
	defer back.Close()
	Configure("", "tok-fb", [][2]string{{"http://127.0.0.1:1", back.URL}})
	defer Configure("", "", nil)

	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/fapi/v1/x", nil)
	resp, err := Client().Do(req)
	if err != nil {
		t.Fatalf("failover 应成功: %v", err)
	}
	resp.Body.Close()
	if req.URL.Host != "127.0.0.1:1" {
		t.Fatalf("原请求 URL 被改写成 %s（应仍为主端）", req.URL.Host)
	}
	if backHost == "" {
		t.Fatal("备端未收到回退请求")
	}
}

// A2（D2）：GetBody 出错 → 不回退（备端零命中），原传输错上抛。
func TestFailoverRT_GetBodyErrNoFallback(t *testing.T) {
	var backHits int
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backHits++
	}))
	defer back.Close()
	Configure("", "tok-fb", [][2]string{{"http://127.0.0.1:1", back.URL}})
	defer Configure("", "", nil)

	req, _ := http.NewRequest(http.MethodGet, "http://127.0.0.1:1/x", strings.NewReader("b"))
	req.GetBody = func() (io.ReadCloser, error) { return nil, io.ErrClosedPipe }
	if _, err := Client().Do(req); err == nil {
		t.Fatal("GetBody 错应上抛原传输错")
	}
	if backHits != 0 {
		t.Fatalf("GetBody 错不应回退备端: %d hits", backHits)
	}
}

// 主端回 500（业务错）不触发回退；非主 host 的传输错也不触发。
func TestFailoverRT_NoFallbackOn5xxOrForeignHost(t *testing.T) {
	var backHits int
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backHits++
	}))
	defer back.Close()
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
	}))
	defer primary.Close()
	Configure("", "tok", [][2]string{{primary.URL, back.URL}})
	defer Configure("", "", nil)

	resp, err := Client().Get(primary.URL + "/x")
	if err != nil || resp.StatusCode != 500 {
		t.Fatalf("5xx 应原样返回: resp=%v err=%v", resp, err)
	}
	resp.Body.Close()
	if _, err := Client().Get("http://127.0.0.1:1/y"); err == nil {
		t.Fatal("非主 host 传输错应原样上抛")
	}
	if backHits != 0 {
		t.Fatalf("备用端点被误触: %d", backHits)
	}
}

// BACKUP 空 → 不装 failoverRT（行为不变）。
func TestFailoverRT_DisabledWhenBackupEmpty(t *testing.T) {
	Configure("", "tok", nil)
	if newFailoverRT(http.DefaultTransport) != nil {
		t.Fatal("backup 空不应装 failoverRT")
	}
}

// A4（D5 host 对表化）：NOX_OKX_PROXY_URL 与 NOX_CEX_WS_BASE host 的传输错
// 均回退 NOX_CEX_WS_BASE_BACKUP；ws:// 基址 env 归一 http 匹配口径。
func TestFailoverRT_HostTable(t *testing.T) {
	var backHits int
	back := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		backHits++
	}))
	defer back.Close()
	bu := strings.TrimPrefix(back.URL, "http://")
	Configure("", "tok-fb", [][2]string{
		{"https://127.0.0.1:1", "http://" + bu}, // okx 代理对（REST 走 http 口径匹配 host）
		{"wss://127.0.0.1:2", "http://" + bu},   // cex 基址对（ws scheme 归一 http 匹配）
	})
	defer Configure("", "", nil)

	if resp, err := Client().Get("https://127.0.0.1:1/api/v5/account"); err != nil {
		t.Fatalf("okx host 应回退成功: %v", err)
	} else {
		resp.Body.Close()
	}
	if backHits != 1 {
		t.Fatalf("okx host 传输错应回退备端 1 次, got %d", backHits)
	}
	if resp, err := Client().Get("http://127.0.0.1:2/gatespotws/x"); err != nil {
		t.Fatalf("cex base host 应回退成功: %v", err)
	} else {
		resp.Body.Close()
	}
	if backHits != 2 {
		t.Fatalf("cex base host 传输错应回退备端, got %d", backHits)
	}
}
