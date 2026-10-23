// fresh_transport_test.go —— exec 返修⑤锚：FreshClient 装配层全继承+fresh 单例。
// 防回归面=exec 侧裸 transport 绕层破口（venue 走代理面/需 CA 即死）。
package httpx

import (
	"sync"
	"testing"
)

func TestFreshTransport_SingletonAndLayers(t *testing.T) {
	proxyToken = "tok-x" // Configure 轻置（本包内可见——单测面）
	defer func() { proxyToken = "" }()
	a, b := FreshClient(), FreshClient()
	if a.Transport == nil {
		t.Fatal("fresh Transport=nil——装配层未继承（裸 transport 破口回归）")
	}
	if a.Transport != b.Transport {
		t.Fatal("fresh RT 应单例——跨单 banGate/failover 状态存续面")
	}
	if _, ok := a.Transport.(banGateRT); !ok {
		t.Fatalf("fresh 最外层应 banGateRT（418 退避闸继承），实得 %T", a.Transport)
	}
	if _, ok := Client().Transport.(*sharedWrap); !ok {
		t.Fatal("shared 形语义漂移——应 sharedWrap 指针壳")
	}
}

func TestFreshTransport_ZeroConfigExplicitFresh(t *testing.T) {
	// 零配置直连形分野：fresh 形 Transport 非 nil（显式 fresh 基座——nil 会走
	// DefaultTransport=keepalive 复用击穿 fresh 语义）vs shared 形 nil（复用面）。
	proxyCAPath, proxyToken = "", ""
	sharedRTOnce, sharedRT = sync.Once{}, nil
	freshRTOnce, freshRT = sync.Once{}, nil
	defer func() {
		sharedRTOnce, sharedRT = sync.Once{}, nil
		freshRTOnce, freshRT = sync.Once{}, nil
	}()
	if f := FreshClient(); f.Transport == nil {
		t.Fatal("零配置 fresh 形 Transport=nil——DefaultTransport 复用击穿 fresh 语义")
	}
	if c := Client(); c.Transport != nil {
		t.Fatal("零配置 shared 形应 nil——两形语义分野破坏")
	}
}
