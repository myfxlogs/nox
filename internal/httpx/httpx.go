// Package httpx 提供访问 Binance 静态代理（D-055）的共享 HTTP 客户端。
// 单一真相源：CA 信任 + 令牌注入只此一处，reconcile（账户）与 collect（行情）共用。
package httpx

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gorilla/websocket"
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
	if fb := newFailoverRT(c.Transport); fb != nil {
		c.Transport = fb
	}
	return c
}

// WSDialer 返回与 Client() 同源的 websocket.Dialer（D-450）：CA 信任 + 令牌头同源注入。
// 返回 dialer 与握手 header（X-Proxy-Token）；env 未配置时均可直连公网 WS。
func WSDialer() (*websocket.Dialer, http.Header) {
	d := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	caPath, tok := os.Getenv("ARB_BINANCE_PROXY_CA"), os.Getenv("ARB_BINANCE_PROXY_TOKEN")
	if caPath != "" {
		if pem, err := os.ReadFile(caPath); err == nil {
			pool, err := x509.SystemCertPool()
			if err != nil {
				pool = x509.NewCertPool()
			}
			if pool.AppendCertsFromPEM(pem) {
				d.TLSClientConfig = &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}
			}
		}
	}
	if tok != "" {
		header.Set("X-Proxy-Token", tok)
	}
	return d, header
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

// failoverRT cex-proxy 双端点 REST 回退（D-301b 预留槽实现）：
// 仅当请求目标 host 命中主端对表（D-519 D5 三对 env 映射）且 RoundTrip 出**传输层错误**时，
// 把请求改发对应备端 host 重试一次（X-Proxy-Token 由内层 tokenRT 重注）。
// 4xx/5xx 是 venue 业务语义不触发；body 不可重放（Body 非 nil 且无 GetBody）不触发。
// 每请求自主端起——主端恢复即自动回主（无粘滞/无探针）。
type failoverRT struct {
	base http.RoundTripper
	// targets D-519 D5：主 host→备 {scheme,host} 对表（构造期收集，host:port 精确匹配，
	// 同 host 去重）。覆盖三对：binance 代理、cex 公共基址（gate 亦经此）、okx 代理
	// （无独立 okx 备 env，统一挂 ARB_CEX_WS_BASE_BACKUP）。
	targets map[string]failoverTarget
}

type failoverTarget struct{ scheme, host string }

// hostEnvURL 解析基址 env：ws/wss scheme 归一 http/https（REST 请求 host 匹配口径）；
// 无 scheme 补 https://。
func hostEnvURL(v string) *url.URL {
	v = strings.TrimSpace(v)
	v = strings.Replace(v, "wss://", "https://", 1)
	v = strings.Replace(v, "ws://", "http://", 1)
	if !strings.Contains(v, "://") {
		v = "https://" + v
	}
	u, err := url.Parse(v)
	if err != nil || u.Host == "" {
		return nil
	}
	return u
}

func newFailoverRT(base http.RoundTripper) http.RoundTripper {
	targets := map[string]failoverTarget{}
	add := func(primEnv, backEnv string) {
		bu := hostEnvURL(os.Getenv(backEnv))
		pu := hostEnvURL(os.Getenv(primEnv))
		if bu == nil || pu == nil {
			return
		}
		if _, ok := targets[pu.Host]; !ok {
			targets[pu.Host] = failoverTarget{bu.Scheme, bu.Host}
		}
	}
	add("BINANCE_PROXY_URL", "BINANCE_PROXY_URL_BACKUP")
	add("ARB_CEX_WS_BASE", "ARB_CEX_WS_BASE_BACKUP")
	add("ARB_OKX_PROXY_URL", "ARB_CEX_WS_BASE_BACKUP")
	if len(targets) == 0 {
		return nil
	}
	return failoverRT{base: base, targets: targets}
}

func (f failoverRT) RoundTrip(r *http.Request) (*http.Response, error) {
	resp, err := f.base.RoundTrip(r)
	t, hit := f.targets[r.URL.Host]
	if err == nil || !hit || r.Context().Err() != nil {
		return resp, err
	}
	// D-519 D3（裁定=方法门控，§1 风控优先）：非幂等方法不回退——binance 签名请求
	// body=nil、参数+签名全在 query，recvWindow 内同签名重放对 venue 有效；主端错误若
	// 发生在「已转发、响应回传途中」回退即二次下单（venue clientOrderId 幂等未实证，
	// 不作安全依据）。已知代价（记录在案）：主端死时 listenKey create/keepalive
	//（POST /v1/listenKey）不回退→binance user stream 停供至主端恢复。
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return resp, err
	}
	if r.Body != nil && r.GetBody == nil {
		return resp, err // body 不可重放
	}
	if r.GetBody != nil {
		if _, berr := r.GetBody(); berr != nil {
			return resp, err // D-519 D2：body 重放失败=不回退，原错上抛（勿发空 body 给备端）
		}
	}
	r2 := r.Clone(r.Context())
	u2 := *r.URL // D-519 D1：URL 值拷贝等价 Clone——请求不可变契约（Clone 浅拷共享 URL 指针；本模块 go directive 门槛下 URL.Clone 不可用）
	r2.URL = &u2
	if r.GetBody != nil {
		r2.Body, _ = r.GetBody() // 上支已验 berr==nil
	}
	r2.URL.Scheme = t.scheme
	r2.URL.Host = t.host
	r2.Host = ""
	return f.base.RoundTrip(r2)
}

// BinanceSapiBase D-106：base_url → sapi 裸 host 基址（调用方自拼 /sapi/... 段）。
// 原生币安域（*.binance.com）恒归 <scheme>://api.binance.com——sapi 只驻该 host，
// fapi 域无 /sapi（404）；且 strings.Replace("/api"→"/sapi") 会命中域名内 "/api"
// 产 sapi.binance.com 假域（SG 实拍 404 HTML）。代理域保留旧派生：裸 host。
func BinanceSapiBase(baseURL string) string {
	if u, err := url.Parse(baseURL); err == nil && strings.HasSuffix(u.Host, "binance.com") {
		return u.Scheme + "://api.binance.com"
	}
	return strings.TrimSuffix(strings.TrimSuffix(baseURL, "/api"), "/fapi")
}

// BinanceApiSegBase D-106：base_url → <seg> 段基址（返回含 /<seg> 后缀——earn 拼 /v1/...、
// capital_gaps 拼 /v1/... 同形）。原生域恒归 <scheme>://<segHost>.binance.com/<seg>
// （sapi→api 域、dapi→dapi 域、eapi→eapi 域——官方文档三 host 分立）；代理域=裸
// host+/seg（代理按前缀路由）。
func BinanceApiSegBase(baseURL, seg string) string {
	if u, err := url.Parse(baseURL); err == nil && strings.HasSuffix(u.Host, "binance.com") {
		host := seg
		if seg == "sapi" {
			host = "api"
		}
		return u.Scheme + "://" + host + ".binance.com/" + seg
	}
	return strings.TrimSuffix(strings.TrimSuffix(baseURL, "/api"), "/fapi") + "/" + seg
}
