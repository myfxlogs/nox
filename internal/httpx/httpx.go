// Package httpx 提供访问 Binance 静态代理（D-055）的共享 HTTP 客户端。
// 单一真相源：CA 信任 + 令牌注入只此一处，reconcile（账户）与 collect（行情）共用。
package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"os"
	"time"
)

// Client 返回带 Binance 静态代理信任的 http.Client：
//   - ARB_BINANCE_PROXY_CA 非空 → 在系统根证书上追加该自签 CA（代理 TLS 才能通过）
//   - ARB_BINANCE_PROXY_TOKEN 非空 → 所有请求自动带 X-Proxy-Token（防开放中继鉴权）
//
// 两者都未配置时返回默认客户端（OKX/Gate 等直连场景）。
func Client() *http.Client {
	c := &http.Client{Timeout: 15 * time.Second}
	caPath, tok := os.Getenv("ARB_BINANCE_PROXY_CA"), os.Getenv("ARB_BINANCE_PROXY_TOKEN")
	if caPath == "" && tok == "" {
		return c
	}
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return c
	}
	tr = tr.Clone()
	if caPath != "" {
		if pem, err := os.ReadFile(caPath); err == nil {
			pool, err := x509.SystemCertPool() // 系统根 + 自签 CA，勿替换（OKX 等仍须信任公网 CA）
			if err != nil {
				pool = x509.NewCertPool()
			}
			if pool.AppendCertsFromPEM(pem) {
				tr.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			}
		}
	}
	c.Transport = tr
	if tok != "" {
		c.Transport = tokenRT{base: tr, tok: tok}
	}
	return c
}

// tokenRT 给每个请求注入 X-Proxy-Token 头（代理防开放中继鉴权，D-055）。
type tokenRT struct {
	base http.RoundTripper
	tok  string
}

func (t tokenRT) RoundTrip(r *http.Request) (*http.Response, error) {
	r2 := r.Clone(r.Context())
	r2.Header.Set("X-Proxy-Token", t.tok)
	return t.base.RoundTrip(r2)
}
