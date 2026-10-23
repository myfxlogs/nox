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
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// 运行期代理配置——D-145 env 收口：装配层 boot 时 Configure 注入（cfg/ secrets 供值），
// 本包不再直读 env。未注入=零值=直连默认（OKX/Gate 公网场景语义不变）。
var (
	proxyCAPath   string
	proxyToken    string
	failoverPairs [][2]string // {主端基址, 备端基址} URL 对（D-519 D5 host 对表源）
)

// Configure 注入代理配置（caPath=自签 CA 路径，token=X-Proxy-Token，
// pairs=REST failover 主备基址对）。boot 先于一切 Client/WSDialer 消费点调用。
func Configure(caPath, token string, pairs [][2]string) {
	proxyCAPath, proxyToken, failoverPairs = caPath, token, pairs
	sharedRTOnce, sharedRT = sync.Once{}, nil // 再注入须重建单例（boot 期幂等/测试隔离面）
	freshRTOnce, freshRT = sync.Once{}, nil   // 同律（exec 返修⑤——fresh 单例随注入重建）
}

// Client 返回带 Binance 静态代理信任的 http.Client：
//   - proxyCAPath 非空 → 在系统根证书上追加该自签 CA（代理 TLS 才能通过）
//   - proxyToken 非空 → 所有请求自动带 X-Proxy-Token（防开放中继鉴权）
//
// 两者都未配置时返回默认客户端（OKX/Gate 等直连场景）。
// sharedRT D-667：Transport 栈进程级单例（Configure 后首 Client() 构建——
// 每调用新建 Transport=逐请求独立 TLS 握手零复用，SG 实测 20conn/s×5.2KB
// ≈104KB/s churn 病灶；共享 RoundTripper=同 host 连接池复用）。
// Timeout 仍在 Client 层各调用自携（传输层共享≠超时策略共享）。
var (
	sharedRTOnce sync.Once
	sharedRT     http.RoundTripper
	freshRTOnce  sync.Once
	freshRT      http.RoundTripper
)

func buildSharedRT() http.RoundTripper {
	if proxyCAPath == "" && proxyToken == "" {
		return nil // 零值=直连——http.Client 无 Transport 走 DefaultTransport 复用面
	}
	rt := assembleRT(false)
	if rt == nil {
		return nil
	}
	return &sharedWrap{rt: rt} // 指针壳=接口可比（锚测单例断言面）
}

// assembleRT 参数化传输装配（exec 返修⑤——sharedRT 装配抽 keepalive 开关）：
// disableKeepAlives=true → exec 钱径 fresh 形（每请求新 conn 不复用）；
// 装配层（自签 CA/tokenRT/failoverRT/banGateRT）两形同源全继承——exec 侧裸
// transport 绕层破口（venue 走代理面/需 CA 即死）由 fresh 形收编。
func assembleRT(disableKeepAlives bool) http.RoundTripper {
	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return nil
	}
	tr = tr.Clone()
	tr.DisableKeepAlives = disableKeepAlives
	// h2 感染清零（SG 产证 10-23）：DefaultTransport 一经使用即懒配 h2
	// （onceSetNextProtoDefaults→TLSNextProto 注 h2 handler + TLSClientConfig.
	// NextProtos 注 "h2"）——Clone 浅继中毒字段则 ALPN 供 h2，对端回 h2 帧
	// 落 h1 解析器（malformed HTTP response \x00\x00\x12\x04 实证）。显式
	// 钉 h1：NextProtos 只供 http/1.1 + TLSNextProto 空表（ForceAttemptHTTP2
	// flag 只挡懒配时机不挡已染字段继承）。
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}
	tc := tr.TLSClientConfig
	if tc == nil {
		tc = &tls.Config{}
	} else {
		tc = tc.Clone()
	}
	tc.NextProtos = []string{"http/1.1"}
	tr.TLSClientConfig = tc
	if proxyCAPath != "" {
		if pem, err := os.ReadFile(proxyCAPath); err == nil {
			pool, err := x509.SystemCertPool() // 系统根 + 自签 CA，勿替换（OKX 等仍须信任公网 CA）
			if err != nil {
				pool = x509.NewCertPool()
			}
			if pool.AppendCertsFromPEM(pem) {
				tc.RootCAs = pool // 并入既有 tc——勿整体替换（会丢 NextProtos h1 钉）
				tc.MinVersion = tls.VersionTLS12
			}
		}
	}
	// D-667 续修③：h1 回退止血——ForceAttemptHTTP2=false 全 conn 走 h1.1
	// （ConfigureTransports 会覆写 flag 使 h2 仍生效=flag 死字段病灶修——
	// 整段删使 flag 真生效；h2 会话面病灶归零。健检码件不入 h1 径）。
	tr.ForceAttemptHTTP2 = false
	var rt http.RoundTripper = tr
	if proxyToken != "" {
		rt = tokenRT{base: rt, tok: proxyToken}
	}
	if fb := newFailoverRT(rt); fb != nil {
		rt = fb
	}
	// 418 感知全局退避闸——最外包（封禁响应层语义先于 failover 传输层尝试；
	// 单闸全 bn REST 消费径同过=封禁自持病灶单点治理）。
	return newBanGateRT(rt)
}

// sharedWrap 指针壳：内层 RT 族含 map 字段不可比——单例断言须指针身分。
type sharedWrap struct{ rt http.RoundTripper }

func (w *sharedWrap) RoundTrip(r *http.Request) (*http.Response, error) { return w.rt.RoundTrip(r) }

func Client() *http.Client {
	c := &http.Client{Timeout: 15 * time.Second}
	sharedRTOnce.Do(func() { sharedRT = buildSharedRT() })
	if sharedRT != nil {
		c.Transport = sharedRT
	}
	return c
}

// FreshClient exec 钱径专用 fresh 形（exec 返修⑤）：base transport DisableKeepAlives
// ＝每单新鲜 conn 不复用（复用流瞬死病灶与钱径脱钩；TLS 每单新握=下单低频域可
// 承受）；装配层与 Client() 同源全继承（自签 CA/X-Proxy-Token/failover/banGate），
// banGate/failover 状态随单例跨单存续（418 退避连续性）。零配置直连形 Transport
// 非 nil——显式 fresh 基座（nil 会走 DefaultTransport=keepalive 复用击穿 fresh 语义）。
func FreshClient() *http.Client {
	c := &http.Client{Timeout: 15 * time.Second}
	freshRTOnce.Do(func() { freshRT = assembleRT(true) })
	if freshRT != nil {
		c.Transport = freshRT
	}
	return c
}

// WSDialer 返回与 Client() 同源的 websocket.Dialer（D-450）：CA 信任 + 令牌头同源注入。
// 返回 dialer 与握手 header（X-Proxy-Token）；env 未配置时均可直连公网 WS。
func WSDialer() (*websocket.Dialer, http.Header) {
	d := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	header := http.Header{}
	caPath, tok := proxyCAPath, proxyToken
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
	// （无独立 okx 备 env，统一挂 NOX_CEX_WS_BASE_BACKUP）。
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
	for _, p := range failoverPairs {
		bu, pu := hostEnvURL(p[1]), hostEnvURL(p[0])
		if bu == nil || pu == nil {
			continue
		}
		if _, ok := targets[pu.Host]; !ok {
			targets[pu.Host] = failoverTarget{bu.Scheme, bu.Host}
		}
	}
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
	// D3（裁定=方法门控，§1 风控优先）：非幂等方法不回退——binance 签名请求
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
