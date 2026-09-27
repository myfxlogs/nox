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
	t.Setenv("BINANCE_PROXY_URL", "http://127.0.0.1:1") // 拒连=传输错
	t.Setenv("BINANCE_PROXY_URL_BACKUP", back.URL)
	t.Setenv("ARB_BINANCE_PROXY_TOKEN", "tok-fb")

	resp, err := Client().Post("http://127.0.0.1:1/fapi/v1/x", "text/plain", strings.NewReader("b"))
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
	t.Setenv("BINANCE_PROXY_URL", primary.URL)
	t.Setenv("BINANCE_PROXY_URL_BACKUP", back.URL)

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
	t.Setenv("BINANCE_PROXY_URL_BACKUP", "")
	if newFailoverRT(http.DefaultTransport) != nil {
		t.Fatal("backup 空不应装 failoverRT")
	}
}
