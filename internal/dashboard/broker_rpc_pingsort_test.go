package dashboard

// D-142 ① 单测：PingHost 延迟预排序。
// 用例：①好 97ms 排首位 ②坏端点剔除（排后） ③ping 全败回退原序 ④nil pingFn 回退原序

import (
	"errors"
	"testing"
)

// 用例①：好 97ms 排首位——多端点中延迟最低的排第一。
func TestPingSortLowestLatencyFirst(t *testing.T) {
	eps := []endpoint{
		{host: "slow.example", port: 443},
		{host: "fast.example", port: 443},
		{host: "mid.example", port: 443},
	}
	sorted := pingSortEndpoints(eps, func(ep endpoint) (int32, error) {
		switch ep.host {
		case "fast.example":
			return 97, nil
		case "mid.example":
			return 200, nil
		case "slow.example":
			return 500, nil
		}
		return 0, errors.New("unknown")
	})
	if sorted[0].host != "fast.example" {
		t.Errorf("first: got %s, want fast.example (97ms)", sorted[0].host)
	}
	if sorted[1].host != "mid.example" {
		t.Errorf("second: got %s, want mid.example (200ms)", sorted[1].host)
	}
	if sorted[2].host != "slow.example" {
		t.Errorf("third: got %s, want slow.example (500ms)", sorted[2].host)
	}
	if sorted[0].latency != 97 {
		t.Errorf("latency: got %d, want 97", sorted[0].latency)
	}
}

// 用例②：坏端点排后——ping 失败的端点保留但排在成功端点之后（原序）。
func TestPingSortFailedEndpointsLast(t *testing.T) {
	eps := []endpoint{
		{host: "bad1.example", port: 443},
		{host: "good.example", port: 443},
		{host: "bad2.example", port: 443},
	}
	sorted := pingSortEndpoints(eps, func(ep endpoint) (int32, error) {
		if ep.host == "good.example" {
			return 97, nil
		}
		return 0, errors.New("timeout")
	})
	if sorted[0].host != "good.example" {
		t.Errorf("first should be good.example, got %s", sorted[0].host)
	}
	// 坏端点按原序排后
	if sorted[1].host != "bad1.example" {
		t.Errorf("second should be bad1.example (original order), got %s", sorted[1].host)
	}
	if sorted[2].host != "bad2.example" {
		t.Errorf("third should be bad2.example (original order), got %s", sorted[2].host)
	}
}

// 用例③：ping 全败 → 回退原顺序（探测是优化不是依赖）。
func TestPingSortAllFailFallbackOriginal(t *testing.T) {
	eps := []endpoint{
		{host: "a.example", port: 443},
		{host: "b.example", port: 443},
		{host: "c.example", port: 443},
	}
	sorted := pingSortEndpoints(eps, func(ep endpoint) (int32, error) {
		return 0, errors.New("gateway unreachable")
	})
	if len(sorted) != 3 {
		t.Fatalf("len: got %d, want 3", len(sorted))
	}
	for i, ep := range sorted {
		if ep.host != eps[i].host {
			t.Errorf("pos %d: got %s, want %s (original order)", i, ep.host, eps[i].host)
		}
	}
}

// 用例④：nil pingFn → 回退原顺序。
func TestPingSortNilPingFnFallback(t *testing.T) {
	eps := []endpoint{
		{host: "a.example", port: 443},
		{host: "b.example", port: 443},
	}
	sorted := pingSortEndpoints(eps, nil)
	if len(sorted) != 2 || sorted[0].host != "a.example" || sorted[1].host != "b.example" {
		t.Errorf("nil pingFn should return original order, got %+v", sorted)
	}
}

// 用例⑤：单端点 → 直接返回（无需排序）。
func TestPingSortSingleEndpointNoSort(t *testing.T) {
	eps := []endpoint{{host: "only.example", port: 443}}
	called := false
	sorted := pingSortEndpoints(eps, func(ep endpoint) (int32, error) {
		called = true
		return 97, nil
	})
	if called {
		t.Error("should not ping single endpoint")
	}
	if sorted[0].host != "only.example" {
		t.Errorf("got %s, want only.example", sorted[0].host)
	}
}
