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
	b := strings.TrimSuffix(strings.TrimSpace(os.Getenv("ARB_CEX_WS_BASE")), "/")
	b = strings.Replace(b, "https://", "wss://", 1)
	b = strings.Replace(b, "http://", "ws://", 1)
	return b
}

// wsViaProxy base 非空 → base+proxyPath（路由前缀随路径名代入）；空 → native 直连端点。
func wsViaProxy(proxyPath, native string) string {
	if b := cexWSBase(); b != "" {
		return b + proxyPath
	}
	return native
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
