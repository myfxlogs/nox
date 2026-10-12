package dashboard

// D-141 S5 单测：多端点 failover + 私网过滤。
// 用例：①多端点首坏次好→取次好 ②全私网→「无公网端点」 ③全失败→错误含尝试数
//      ④parsePublicEndpoints 过滤+域名/IPv6 ⑤isPrivateOrLoopback RFC1918+环回

import (
	"errors"
	"strings"
	"testing"
)

// 用例①：多端点首坏次好→取次好（failoverVerify 首个 connectFn 失败→取次个成功）。
func TestFailoverFirstBadSecondGood(t *testing.T) {
	eps := []endpoint{{host: "bad.example:443"}, {host: "good.example:443"}}
	calls := 0
	r, err := failoverVerify(eps, func(ep endpoint) (failoverResult, error) {
		calls++
		if ep.host == "bad.example:443" {
			return failoverResult{}, errors.New("connection refused")
		}
		return failoverResult{balance: 100, host: ep.host, port: 443}, nil
	})
	if err != nil {
		t.Fatalf("expected success, got err: %v", err)
	}
	if r.host != "good.example:443" {
		t.Errorf("winning host: got %s, want good.example:443", r.host)
	}
	if r.balance != 100 {
		t.Errorf("balance: got %v, want 100", r.balance)
	}
	if calls != 2 {
		t.Errorf("connectFn calls: got %d, want 2 (tried bad then good)", calls)
	}
}

// 用例②：全私网→明确报错「无公端点」（parsePublicEndpoints）。
func TestAllPrivateNoPublicEndpoint(t *testing.T) {
	host := "10.20.111.43:443, 192.168.1.1:443, 127.0.0.1:443"
	_, err := parsePublicEndpoints(host, 443)
	if err == nil {
		t.Fatal("expected error for all-private endpoints, got nil")
	}
	if !strings.Contains(err.Error(), "无公网端点") {
		t.Errorf("error should contain「无公网端点」, got: %v", err)
	}
}

// 用例③：全失败→错误含尝试数（failoverVerify 全部 connectFn 失败）。
func TestAllFailErrorContainsAttemptCount(t *testing.T) {
	eps := []endpoint{{host: "a:443"}, {host: "b:443"}, {host: "c:443"}}
	_, err := failoverVerify(eps, func(ep endpoint) (failoverResult, error) {
		return failoverResult{}, errors.New("timeout")
	})
	if err == nil {
		t.Fatal("expected error for all-fail, got nil")
	}
	if !strings.Contains(err.Error(), "3") {
		t.Errorf("error should contain attempt count 3, got: %v", err)
	}
	if !strings.Contains(err.Error(), "端点") {
		t.Errorf("error should mention 端点, got: %v", err)
	}
}

// 用例④：parsePublicEndpoints 过滤私网保留公网 + 域名视为公网 + IPv6 私网过滤。
func TestParsePublicEndpointsFiltering(t *testing.T) {
	// 私网+公网混合 → 只剩公网
	eps, err := parsePublicEndpoints("10.20.111.43:443, 203.0.113.5:443", 443)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(eps) != 1 || eps[0].host != "203.0.113.5" {
		t.Errorf("expected only 203.0.113.5, got %+v", eps)
	}
	// 域名 → 视为公网（不过滤）
	eps2, err := parsePublicEndpoints("broker.example.com:443", 443)
	if err != nil {
		t.Fatalf("domain err: %v", err)
	}
	if len(eps2) != 1 || eps2[0].host != "broker.example.com" {
		t.Errorf("domain should be public, got %+v", eps2)
	}
	// IPv6 环回 ::1 → 过滤
	eps3, err := parsePublicEndpoints("[::1]:443, 203.0.113.9:443", 443)
	if err != nil {
		t.Fatalf("ipv6 err: %v", err)
	}
	if len(eps3) != 1 || eps3[0].host != "203.0.113.9" {
		t.Errorf("expected only 203.0.113.9 (::1 filtered), got %+v", eps3)
	}
	// 单端点无端口 → 用 defaultPort
	eps4, err := parsePublicEndpoints("203.0.113.7", 443)
	if err != nil {
		t.Fatalf("no-port err: %v", err)
	}
	if eps4[0].port != 443 {
		t.Errorf("default port: got %d, want 443", eps4[0].port)
	}
}

// 用例⑤：isPrivateOrLoopback RFC1918 + 环回 + 域名。
func TestIsPrivateOrLoopback(t *testing.T) {
	private := []string{"10.0.0.1", "10.20.111.43", "172.16.0.1", "172.31.255.255", "192.168.1.1", "127.0.0.1", "127.1.2.3", "[::1]"}
	for _, h := range private {
		if !isPrivateOrLoopback(h) {
			t.Errorf("expected private/loopback true for %s", h)
		}
	}
	public := []string{"203.0.113.5", "8.8.8.8", "broker.example.com", "mt5grpc.mtapi.io"}
	for _, h := range public {
		if isPrivateOrLoopback(h) {
			t.Errorf("expected private/loopback false for %s", h)
		}
	}
	// 172.32.x 不在私网段（172.16/12 = 172.16.0.0–172.31.255.255）
	if isPrivateOrLoopback("172.32.0.1") {
		t.Errorf("172.32.0.1 should be public (outside 172.16/12)")
	}
}
