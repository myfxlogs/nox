// WS 代理化拨号公共件（止血·D-511 前导）：NOX_CEX_WS_BASE 置位=三所 WS 全部改经
// cex-proxy（如 wss://<cex-proxy域名>，token 握口头+CA 信任与 httpx.Client 同源）；
// 空=原生直连（默认行为不变）。
// 路由前缀在 tools/cex-proxy config 注册：/okxws（strip）→ws.okx.com、
// /gatespotws（strip）→api.gateio.ws、/v4/ws（透传）→fx-ws.gateio.ws。
package exchange

import (
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nox/internal/platform/httpx"

	"github.com/gorilla/websocket"
)

// cexWSBase 注入基址归一 ws/wss scheme（空=直连模式）。
func cexWSBase() string {
	return normWSBase(ep.CexWSBase)
}

func normWSBase(b string) string {
	b = strings.TrimSuffix(strings.TrimSpace(b), "/")
	b = strings.Replace(b, "https://", "wss://", 1)
	b = strings.Replace(b, "http://", "ws://", 1)
	return b
}

// cexWSBases 有序候选基址表：注入主基址 + 备基址（可空）。
// 空表=直连模式；dial 期 failover 逐序试拨（每连接尝试自主端起——主端恢复即自动回主）。
func cexWSBases() []string {
	var bases []string
	for _, raw := range []string{ep.CexWSBase, ep.CexWSBaseBackup} {
		if b := normWSBase(raw); b != "" {
			bases = append(bases, b)
		}
	}
	// M4：主空+备置错配——候选 URL 会走无 token native dialer 必败；
	// warn 一行不 fail（保留配置自由度）。
	if ep.CexWSBase == "" && len(bases) > 0 {
		log.Printf("[ws-proxy] warn: cex ws 主基址空但备基址置位——备端候选无 token 直连必败")
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

// swapURLHost 把 raw URL 的 host 换成 backupBase 的 host（scheme/path/query 不变）；
// 基址空/任一侧非法 → ""（=无备用候选，调用方零行为变化）。D-519 D4：gate 私有流复用。
func swapURLHost(raw, backupBase string) string {
	back := strings.TrimSpace(backupBase)
	if back == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	bu, err := url.Parse(back)
	if err != nil || bu.Host == "" {
		return ""
	}
	u.Host = bu.Host
	return u.String()
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
