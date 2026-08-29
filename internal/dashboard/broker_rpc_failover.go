// D-141 S5：broker 接入多端点 failover + 私网过滤。
// mtapi 目录 access 首端点常为内网脏数据（Pepperstone-Demo=10.20.111.43 实证），
// 故 VerifyAccount/AddBroker 收到逗号分隔多端点串后：滤 RFC1918+环回 → 依次试连 → 首个成功者胜出。
package dashboard

import (
	"fmt"
	"net"
	"strconv"
	"strings"
)

// endpoint 单个 broker 服务器端点（host:port）。
type endpoint struct {
	host string
	port int32
}

// parsePublicEndpoints 解析逗号分隔的 access 串，滤除 RFC1918 私网与环回地址。
// 返回公网端点列表；全私网/空 → error「无公网端点」。
// 单端点串（无逗号）且为公网 → 单元素列表（向后兼容旧调用）。
// 域名（非 IP）视为公网——broker 服务器可能用域名而非 IP。
func parsePublicEndpoints(host string, defaultPort int32) ([]endpoint, error) {
	parts := strings.Split(host, ",")
	var eps []endpoint
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		h, port := splitHostPort(p, defaultPort)
		if isPrivateOrLoopback(h) {
			continue
		}
		eps = append(eps, endpoint{host: h, port: port})
	}
	if len(eps) == 0 {
		return nil, fmt.Errorf("无公网端点（输入 %d 段，全被私网/环回过滤）", len(parts))
	}
	return eps, nil
}

// splitHostPort 解析 "host:port" 或 "host"（用 defaultPort）。容错 IPv6 [::1]:443。
func splitHostPort(s string, defaultPort int32) (string, int32) {
	if strings.HasPrefix(s, "[") {
		if idx := strings.LastIndex(s, "]:"); idx > -1 {
			if p, err := strconv.Atoi(s[idx+2:]); err == nil {
				return s[:idx+1], int32(p)
			}
		}
		return s, defaultPort
	}
	if idx := strings.LastIndex(s, ":"); idx > 0 {
		if p, err := strconv.Atoi(s[idx+1:]); err == nil {
			return s[:idx], int32(p)
		}
	}
	return s, defaultPort
}

// isPrivateOrLoopback 判断是否为 RFC1918 私网或环回地址（含 IPv6 ::1）。
// net.IP.IsPrivate 覆盖 10/8、172.16/12、192.168/16 及其他私网段；IsLoopback 覆盖 127/8、::1。
// 非 IP（域名）→ false（视为公网）。
func isPrivateOrLoopback(host string) bool {
	h := strings.Trim(host, "[]")
	if ip := net.ParseIP(h); ip != nil {
		return ip.IsLoopback() || ip.IsPrivate()
	}
	return false
}

// failoverVerify D-141 S5：依次试连端点，首个 connectFn 成功者胜出。
// connectFn 尝试单端点连接+验证，返回结果或 error（失败记 lastErr 继续下一个）。
// 全失败 → error 含尝试端点数（「尝试 N 端点」）。
// 提取为独立函数以便单测注入 mock connectFn（首坏次好 / 全失败含尝试数）。
func failoverVerify(eps []endpoint, connectFn func(ep endpoint) (failoverResult, error)) (failoverResult, error) {
	var lastErr error
	for _, ep := range eps {
		r, err := connectFn(ep)
		if err == nil {
			return r, nil
		}
		lastErr = err
	}
	return failoverResult{}, fmt.Errorf("尝试 %d 端点全失败: %w", len(eps), lastErr)
}

// failoverResult failover 试连结果（MT4/MT5 共用，verify 填充后回传）。
type failoverResult struct {
	balance  float64
	equity   float64
	currency string
	leverage int32
	host     string
	port     int32
}
