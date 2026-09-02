package safego

import (
	"sync"
	"testing"
)

// TestGo_PanicRecovered safego.Go 子 goroutine panic 不崩测试进程 + 恢复后 goroutine 终止。
func TestGo_PanicRecovered(t *testing.T) {
	var wg sync.WaitGroup
	wg.Add(1)
	Go("test:panic", func() {
		defer wg.Done()
		panic("test panic")
	})
	wg.Wait() // panic 恢复后 wg.Done 被调用（defer 在 recover 前）
	// 到这里说明进程存活（panic 未崩）
}

// TestGo_NormalRun 正常 fn 执行完毕。
func TestGo_NormalRun(t *testing.T) {
	done := make(chan struct{})
	Go("test:normal", func() {
		close(done)
	})
	<-done
}
