// cex-proxy: 静态 IP CEX API 通用反向代理（D-055 升级 · 配置驱动）
//
// 背景：arb 服务器出口 IP 不固定或被 CEX 地区限制（如 Binance 对美区 IP 返回 451）。
// 本代理部署在固定公网 IP 的机器上，把 arb 的 CEX REST 请求转发到各交易所——
// 交易所看到的源 IP 即代理机固定 IP，可在 key 白名单中锁定。
//
// 特性：
//   - JSON 配置驱动上游路由，加新 CEX = 加一行配置，不改代码不重新编译
//   - 最长前缀匹配（/api/v5 命中 OKX 先于 /api 命中 Binance，零冲突）
//   - 自签 CA + 服务器证书首次运行自动生成到 ./certs/（私钥不落 git）
//   - GET /ca-cert 返回 CA PEM，供客户端一次性信任（bootstrap）
//   - 令牌鉴权（-token，缺省自动生成并打印）：客户端须带 X-Proxy-Token
//   - strip_prefix 选项：剥离路径前缀后再转发（如 /demo/api/v3/... → /api/v3/...）
//
// 用法：cex-proxy -listen=:8443 [-config=config.json] [-token=xxx] [-sans=192.229.85.35]
// 自建基础设施，仅用于访问自家账户数据，绕开 CEX 对特定地区 IP 的限制。
package main

import (
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"sort"
	"strings"
)

// Upstream 定义一条前缀路由规则。
type Upstream struct {
	Prefix      string `json:"prefix"`       // 路径前缀，如 /api/v5、/fapi、/api
	Target      string `json:"target"`       // 上游 base URL，如 https://www.okx.com
	StripPrefix bool   `json:"strip_prefix"` // true 则剥离 prefix 后再转发
}

// Config 是 cex-proxy 的 JSON 配置文件结构。
type Config struct {
	Upstreams []Upstream `json:"upstreams"`
}

// defaultConfig 与原 binance-proxy 行为完全等价，含 OKX + Gate 扩展。
// 无配置文件时使用此默认值，保证向后兼容。
func defaultConfig() Config {
	return Config{Upstreams: []Upstream{
		{Prefix: "/api/v5", Target: "https://www.okx.com"},
		{Prefix: "/api/v4", Target: "https://api.gateio.ws"},
		{Prefix: "/demofapi", Target: "https://demo-fapi.binance.com", StripPrefix: true},
		{Prefix: "/demo", Target: "https://demo-api.binance.com", StripPrefix: true},
		{Prefix: "/fapi", Target: "https://fapi.binance.com"},
		{Prefix: "/sapi", Target: "https://api.binance.com"},
		{Prefix: "/api", Target: "https://api.binance.com"},
	}}
}

// loadConfig 从 path 读取 JSON 配置；path 为空或文件不存在则返回默认配置。
func loadConfig(path string) (Config, error) {
	if path == "" {
		return defaultConfig(), nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			log.Printf("配置文件 %s 不存在，使用内置默认配置", path)
			return defaultConfig(), nil
		}
		return Config{}, err
	}
	var cfg Config
	if err := json.Unmarshal(b, &cfg); err != nil {
		return Config{}, fmt.Errorf("解析配置文件 %s: %w", path, err)
	}
	if len(cfg.Upstreams) == 0 {
		return Config{}, fmt.Errorf("配置文件 %s 无 upstreams 条目", path)
	}
	return cfg, nil
}

func main() {
	listen := flag.String("listen", ":8443", "监听地址")
	configPath := flag.String("config", "config.json", "配置文件路径（不存在则用内置默认）")
	token := flag.String("token", "", "代理令牌；空则自动生成并打印")
	sans := flag.String("sans", "", "证书 SAN（逗号分隔的 IP/DNS）")
	flag.Parse()

	if *token == "" {
		if *token = loadOrCreateToken(); *token == "" {
			log.Fatal("无法生成/读取代理令牌")
		}
	}

	certFile, keyFile, err := ensureCerts(*sans)
	if err != nil {
		log.Fatal(err)
	}
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		log.Fatal(err)
	}

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatal(err)
	}
	handler, err := newHandler(*token, cfg)
	if err != nil {
		log.Fatal(err)
	}

	srv := &http.Server{
		Addr:      *listen,
		Handler:   handler,
		TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
	}
	log.Printf("cex-proxy 监听 %s (TLS), %d 条上游路由", *listen, len(cfg.Upstreams))
	log.Fatal(srv.ListenAndServeTLS("", ""))
}

// loadOrCreateToken 从 token.txt 读取令牌；不存在则生成并落盘。
func loadOrCreateToken() string {
	const file = "token.txt"
	if b, err := os.ReadFile(file); err == nil {
		tok := strings.TrimSpace(string(b))
		if tok != "" {
			return tok
		}
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		log.Fatal(err)
	}
	tok := hex.EncodeToString(b)
	if err := os.WriteFile(file, []byte(tok+"\n"), 0o600); err != nil {
		log.Fatal(err)
	}
	log.Printf("已生成代理令牌并保存到 %s（客户端须带 X-Proxy-Token 头）: %s", file, tok)
	return tok
}

// route 是解析后的上游路由：target URL + 原始 Upstream 配置。
type route struct {
	target *url.URL
	up     Upstream
}

// newHandler 组装路由：/ca-cert 公开；其余请求过令牌闸后按最长前缀匹配反代。
// 前缀按长度降序排列，保证 /api/v5 先于 /api 匹配（OKX vs Binance）。
func newHandler(token string, cfg Config) (http.Handler, error) {
	routes := make([]route, 0, len(cfg.Upstreams))
	for _, up := range cfg.Upstreams {
		u, err := url.Parse(up.Target)
		if err != nil {
			return nil, fmt.Errorf("解析上游 target %s: %w", up.Target, err)
		}
		routes = append(routes, route{target: u, up: up})
	}
	// 最长前缀优先——防 /api/v5 被 /api 抢先命中。
	sort.Slice(routes, func(i, j int) bool {
		return len(routes[i].up.Prefix) > len(routes[j].up.Prefix)
	})

	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		// Rewrite 不会自动添加 X-Forwarded-*（Director 会自动加回，导致 Binance
		// 白名单看到客户端 IP 而非代理 IP → -2015）。CEX 白名单按 TCP 源 IP 校验，
		// 代理服务器 IP 即为白名单 IP，不需透传客户端 IP。
		r.Out.Header.Del("X-Forwarded-For")
		r.Out.Header.Del("X-Forwarded-Host")
		r.Out.Header.Del("X-Forwarded-Proto")
		r.Out.Header.Del("X-Real-Ip")
		path := r.Out.URL.Path
		for _, rt := range routes {
			if !strings.HasPrefix(path, rt.up.Prefix) {
				continue
			}
			r.Out.URL.Scheme = rt.target.Scheme
			r.Out.URL.Host = rt.target.Host
			r.Out.Host = rt.target.Host // CEX/CDN 按 Host 路由，必须改写
			if rt.up.StripPrefix {
				r.Out.URL.Path = strings.TrimPrefix(path, rt.up.Prefix)
			}
			return
		}
		// 无匹配前缀 → ErrorHandler 兜底 404。
		r.Out.URL.Scheme = ""
		r.Out.URL.Host = ""
	}}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		if r.URL.Host == "" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, err.Error(), http.StatusBadGateway)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ca-cert", func(w http.ResponseWriter, r *http.Request) {
		http.ServeFile(w, r, "certs/ca.pem")
	})
	mux.Handle("/", tokenGate(token, proxy))
	return mux, nil
}

// tokenGate 拦截无令牌请求，防开放中继。
func tokenGate(token string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Proxy-Token") != token {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ensureCerts/writePEM 已拆至 certs.go（D-309 纯移动）。
