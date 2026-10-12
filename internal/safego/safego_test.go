package safego

import (
	"sync"
	"testing"
	"time"
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

// TestGoLoop_PanicRestarts D-115 A1：panic 后 fn 被重启（首调 panic、次调正常返回即终）。
func TestGoLoop_PanicRestarts(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	done := make(chan struct{})
	GoLoop("test:loop-restart", func() {
		mu.Lock()
		calls++
		c := calls
		mu.Unlock()
		if c == 1 {
			panic("first call panic")
		}
		close(done)
	})
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("GoLoop 未在 panic 后重启 fn")
	}
	mu.Lock()
	defer mu.Unlock()
	if calls < 2 {
		t.Fatalf("calls=%d, want >=2（panic 后应重启）", calls)
	}
}

// TestGoLoop_NormalReturnNoRestart A2：fn 正常返回（ctx 取消语义）不重启。
func TestGoLoop_NormalReturnNoRestart(t *testing.T) {
	var mu sync.Mutex
	calls := 0
	GoLoop("test:loop-normal", func() {
		mu.Lock()
		calls++
		mu.Unlock()
	})
	time.Sleep(300 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("calls=%d, want 1（正常返回不应重启）", calls)
	}
}

// TestGoLoop_OnPanicFires A3：Go panic → OnPanic(willRestart=false)；GoLoop panic →
// OnPanic(willRestart=true)。钩子在 panic 帧内同步触发（recover 后、返回前）。
func TestGoLoop_OnPanicFires(t *testing.T) {
	var mu sync.Mutex
	type ev struct {
		name    string
		restart bool
	}
	var got []ev
	SetOnPanic(func(name string, r any, willRestart bool) {
		mu.Lock()
		got = append(got, ev{name, willRestart})
		mu.Unlock()
	})
	defer SetOnPanic(nil)

	done := make(chan struct{})
	var loopOnce sync.Once
	GoLoop("test:loop-hook", func() {
		loopOnce.Do(func() { panic("boom") }) // 首调 panic（验 hook+重启），次调正常返回自终
	})
	Go("test:go-hook", func() {
		defer close(done)
		panic("boom")
	})
	<-done
	time.Sleep(300 * time.Millisecond) // GoLoop 退避首拍 1s 内已触发 hook
	mu.Lock()
	defer mu.Unlock()
	var sawGo, sawLoop bool
	for _, e := range got {
		if e.name == "test:go-hook" && !e.restart {
			sawGo = true
		}
		if e.name == "test:loop-hook" && e.restart {
			sawLoop = true
		}
	}
	if !sawGo || !sawLoop {
		t.Fatalf("OnPanic 缺报: got=%v（want Go restart=false + GoLoop restart=true）", got)
	}
}
