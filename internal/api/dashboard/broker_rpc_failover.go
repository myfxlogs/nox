// D-141 S5：broker 接入多端点 failover + 私网过滤。
// D-142 ①：PingHost 延迟预排序——failoverVerify 前对公网端点逐个 PingHost，按延迟升序排列。
// mtapi 目录 access 首端点常为内网脏数据（Pepperstone-Demo=10.20.111.43 实证），
// 故 VerifyAccount/AddBroker 收到逗号分隔多端点串后：滤 RFC1918+环回 → PingHost 预排序 → 依次试连 → 首个成功者胜出。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	mt5pb "nox/internal/data/adapter/gen/mtapi/mt5"

	"google.golang.org/grpc"
)

// endpoint 单个 broker 服务器端点（host:port）。
type endpoint struct {
	host    string
	port    int32
	latency int32 // D-142 ①：PingHost 延迟（ms），0 = 未探测/MT4 无 PingHost
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
	latency  int32 // D-142 ①：胜出端点 PingHost 延迟（ms），0 = 未探测
}

// pingSortEndpoints 对端点列表做 PingHost 延迟预排序（D-142 ①，纯函数可单测）。
// pingFn 对单个端点探测延迟（ms），error = 该端点探测失败。
// 成功探测的端点按延迟升序排前，失败端点按原序排后；
// 全部失败或 pingFn=nil → 返回原顺序（探测是优化不是依赖）。
func pingSortEndpoints(eps []endpoint, pingFn func(endpoint) (int32, error)) []endpoint {
	if len(eps) <= 1 || pingFn == nil {
		return eps
	}
	type scored struct {
		ep      endpoint
		latency int32
		ok      bool
		idx     int
	}
	scoredEps := make([]scored, len(eps))
	anyOK := false
	for i, ep := range eps {
		lat, err := pingFn(ep)
		if err == nil && lat >= 0 {
			scoredEps[i] = scored{ep: ep, latency: lat, ok: true, idx: i}
			anyOK = true
		} else {
			scoredEps[i] = scored{ep: ep, latency: -1, ok: false, idx: i}
		}
	}
	if !anyOK {
		return eps // 全失败 → 回退原顺序
	}
	sort.SliceStable(scoredEps, func(i, j int) bool {
		if scoredEps[i].ok != scoredEps[j].ok {
			return scoredEps[i].ok // 成功排前
		}
		if !scoredEps[i].ok {
			return scoredEps[i].idx < scoredEps[j].idx // 失败按原序
		}
		return scoredEps[i].latency < scoredEps[j].latency // 成功按延迟升序
	})
	out := make([]endpoint, len(scoredEps))
	for i, s := range scoredEps {
		s.ep.latency = s.latency
		out[i] = s.ep
	}
	return out
}

// pingSortViaGateway 通过 mtapi 网关 PingHost 对端点做延迟预排序（D-142 ①）。
// MT5 走 mt5Gateway（D-470：MT4 已退役，platform 仅认 1）。临时拨号即查即断（同 dialMT 模式）。
// 网关不可达或全失败 → 返回原顺序（探测是优化不是依赖）。
func pingSortViaGateway(ctx context.Context, platform int32, eps []endpoint) []endpoint {
	if len(eps) <= 1 {
		return eps
	}
	gateway := mt5Gateway
	conn, err := dialMT(ctx, gateway)
	if err != nil {
		log.Printf("[broker] ping dial %s: %v (skip pre-sort)", gateway, err)
		return eps
	}
	defer conn.Close()
	pingFn := newPingFn(platform, conn)
	if pingFn == nil {
		return eps
	}
	return pingSortEndpoints(eps, pingFn)
}

// newPingFn 构造 PingHost 探测闭包（单端点 3s 超时）。nil = 未知 platform。
func newPingFn(platform int32, conn *grpc.ClientConn) func(endpoint) (int32, error) {
	switch platform {
	case 1:
		c := mt5pb.NewServiceClient(conn)
		return func(ep endpoint) (int32, error) {
			pctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			port := ep.port
			reply, err := c.PingHost(pctx, &mt5pb.PingHostRequest{Host: ep.host, Port: &port})
			if err != nil {
				return 0, err
			}
			if e := reply.GetError(); e != nil {
				return 0, fmt.Errorf("ping: %s", e.GetMessage())
			}
			return reply.GetResult(), nil
		}
	default:
		return nil
	}
}
