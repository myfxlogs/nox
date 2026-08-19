package bus

import "sync"

// Topic 通用 pub/sub（D-049）：慢消费者丢旧保新（drain-then-replace，与 QuoteBus.Publish 同策略）。
// 用于 PersonalMonitor 实时现价广播 → StreamUpdates 多前端扇出。
type Topic[T any] struct {
	mu   sync.Mutex
	subs []chan T
}

// NewTopic 创建通用 Topic。
func NewTopic[T any]() *Topic[T] {
	return &Topic[T]{}
}

// Subscribe 返回接收消息的 channel；cancel 解订阅并关闭 channel。
func (t *Topic[T]) Subscribe() (<-chan T, func()) {
	ch := make(chan T, 1)
	t.mu.Lock()
	t.subs = append(t.subs, ch)
	t.mu.Unlock()
	return ch, func() {
		t.mu.Lock()
		defer t.mu.Unlock()
		for i, c := range t.subs {
			if c == ch {
				t.subs = append(t.subs[:i], t.subs[i+1:]...)
				break
			}
		}
		close(ch)
	}
}

// Publish 发布消息到所有订阅 channel；channel 满时丢旧保新。
func (t *Topic[T]) Publish(v T) {
	t.mu.Lock()
	subs := append([]chan T(nil), t.subs...)
	t.mu.Unlock()
	for _, ch := range subs {
		select {
		case ch <- v:
		default:
			select {
			case <-ch:
			default:
			}
			select {
			case ch <- v:
			default:
			}
		}
	}
}
