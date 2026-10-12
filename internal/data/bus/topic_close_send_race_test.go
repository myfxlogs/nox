// d108_p1_test.go —— D-108 P1 A4：总线 close/send 竞态 panic 单测（先红后绿）。
// 审计场景：发布 goroutine × 前端断开 cancel → send on closed channel panic。
// 修复后（subEntry.mu 同步 close/send + sync.Once + closed 标志）不 panic 且 race-clean。
package bus

import (
	"sync"
	"testing"
)

// TestD108_TopicCloseSendRace 并发 Publish × cancel → 不 panic（subEntry.mu 同步 close/send）。
func TestD108_TopicCloseSendRace(t *testing.T) {
	topic := NewTopic[int]()
	_, cancel := topic.Subscribe()

	var wg sync.WaitGroup
	// 并发发布者：高频 Publish，部分会命中 cancel 后的 closed channel
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				topic.Publish(n*1000 + j)
			}
		}(i)
	}
	// 并发 cancel：中途取消订阅（close channel）
	cancelWG := sync.WaitGroup{}
	cancelWG.Add(1)
	go func() {
		defer cancelWG.Done()
		cancel()
	}()
	cancelWG.Wait()
	wg.Wait()
	// 到这里不 panic = 通过
}

// TestD108_TopicDoubleCancel 重复 cancel 不 panic（sync.Once 保护 close）。
func TestD108_TopicDoubleCancel(t *testing.T) {
	topic := NewTopic[int]()
	_, cancel := topic.Subscribe()
	cancel() // 第一次 close
	cancel() // 第二次 close（sync.Once 应跳过，不 panic）
}

// TestD108_TopicPublishAfterCancel cancel 后 Publish 不 panic（closed 标志跳过 send）。
func TestD108_TopicPublishAfterCancel(t *testing.T) {
	topic := NewTopic[int]()
	_, cancel := topic.Subscribe()
	cancel()
	// cancel 后 Publish：channel 已 close，Publish 侧 closed 标志检查应跳过 send
	topic.Publish(42)
}
