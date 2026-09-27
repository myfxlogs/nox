// WS 代理化拨号公共件（止血·D-511 前导）：ARB_CEX_WS_BASE 置位=三所 WS 全部改经
// cex-proxy（如 wss://192.229.85.35:8443，token 握口头+CA 信任与 httpx.Client 同源）；
// 空=原生直连（默认行为不变）。
// 路由前缀在 tools/cex-proxy config 注册：/okxws（strip）→ws.okx.com、
// /gatespotws（strip）→api.gateio.ws、/v4/ws（透传）→fx-ws.gateio.ws。
package exchange

import (
	"net/http"
	"os"
	"strings"
	"time"

	"arb/internal/httpx"

	"github.com/gorilla/websocket"
)

// cexWSBase 读 ARB_CEX_WS_BASE 并归一 ws/wss scheme（空=直连模式）。
func cexWSBase() string {
	return normWSBase(os.Getenv("ARB_CEX_WS_BASE"))
}

func normWSBase(b string) string {
	b = strings.TrimSuffix(strings.TrimSpace(b), "/")
	b = strings.Replace(b, "https://", "wss://", 1)
	b = strings.Replace(b, "http://", "ws://", 1)
	return b
}

// cexWSBases 有序候选基址表：ARB_CEX_WS_BASE 主 + ARB_CEX_WS_BASE_BACKUP 备（可空）。
// 空表=直连模式；dial 期 failover 逐序试拨（每连接尝试自主端起——主端恢复即自动回主）。
func cexWSBases() []string {
	var bases []string
	for _, env := range []string{"ARB_CEX_WS_BASE", "ARB_CEX_WS_BASE_BACKUP"} {
		if b := normWSBase(os.Getenv(env)); b != "" {
			bases = append(bases, b)
		}
	}
	return bases
}

// wsViaProxy base 非空 → base+proxyPath（路由前缀随路径名代入）；空 → native 直连端点。
func wsViaProxy(proxyPath, native string) string {
	if b := cexWSBase(); b != "" {
		return b + proxyPath
	}
	return native
}

// wsViaProxyURLs proxyPath → 有序候选 URL 表（每基址一份，主端在前）；无基址 → [native]。
func wsViaProxyURLs(proxyPath, native string) []string {
	bases := cexWSBases()
	if len(bases) == 0 {
		return []string{native}
	}
	urls := make([]string, len(bases))
	for i, b := range bases {
		urls[i] = b + proxyPath
	}
	return urls
}

// wsDialer 代理模式 → httpx.WSDialer（CA 信任+X-Proxy-Token 头，D-450 同源）；
// 直连模式 → 原 dialer 形态+nil header（token 头不泄给原生 venue 域）。
func wsDialer(proxied bool) (*websocket.Dialer, http.Header) {
	if proxied {
		return httpx.WSDialer()
	}
	return &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 10 * time.Second,
	}, nil
}
