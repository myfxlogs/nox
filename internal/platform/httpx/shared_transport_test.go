// shared_transport_test.go —— D-667 锚：Client() 共享 Transport 单例
// （每调用新建 Transport=churn 病灶——同 host 连接池须跨 client 复用）。
package httpx

import "testing"

func TestSharedTransport_Singleton(t *testing.T) {
	proxyToken = "tok-x" // Configure 轻置（本包内可见——单测面）
	defer func() { proxyToken = "" }()
	a, b := Client(), Client()
	if a.Transport == nil || a.Transport != b.Transport {
		t.Fatalf("Transport 应单例共享: %v vs %v", a.Transport, b.Transport)
	}
}
