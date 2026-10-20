package exchange

import (
	"testing"
	"time"
)

// TestDepthTimeoutPerVenue depth REST 超时分 venue 配置化锚：
// 配面有 venue=专客户端带预算；未配面回退共享 Client 零行为变。
func TestDepthTimeoutPerVenue(t *testing.T) {
	c := DefaultConfig([]string{"BTC"})
	// 缺省三 venue 8s 带病预算（env 未设径）
	for _, v := range []string{"gate", "okx", "binance"} {
		cl := c.depthClient(v)
		if cl.Timeout != 8*time.Second {
			t.Fatalf("%s 缺省预算应 8s got %v", v, cl.Timeout)
		}
	}
	// 未配 venue 回退共享 Client（零行为变对版）
	if cl := c.depthClient("unknownvenue"); cl != c.Client {
		t.Fatal("未配 venue 应回退共享 Client")
	}
	// 实例化缓存同客复用
	g1, g2 := c.depthClient("gate"), c.depthClient("gate")
	if g1 != g2 {
		t.Fatal("depthClients 缓存应复用同实例")
	}
}
