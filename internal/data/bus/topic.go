package bus

import (
	"sync"
)

// Topic 通用 pub/sub（D-049）：慢消费者丢旧保新（drain-then-replace，与 QuoteBus.Publish 同策略）。
// 用于 PersonalMonitor 实时现价广播 → StreamUpdates 多前端扇出。
// A4：close/send 竞态防护——每个 subEntry 独立 mutex 同步 close 与 send，
// cancel 用 sync.Once 保护 close（重复 cancel 不 panic），Publish 侧 send 前
// 持 subEntry.mu 检查 closed 标志（race-clean，不依赖 recover）。
type Topic[T any] struct {
	mu   sync.Mutex
	subs []*subEntry[T]
}

type subEntry[T any] struct {
	ch     chan T
	mu     sync.Mutex
	closed bool
	once   sync.Once
}

// NewTopic 创建通用 Topic。
func NewTopic[T any]() *Topic[T] {
	return &Topic[T]{}
}

// Subscribe 返回接收消息的 channel；cancel 解订阅并关闭 channel。
func (t *Topic[T]) Subscribe() (<-chan T, func()) {
	entry := &subEntry[T]{ch: make(chan T, 1)}
	t.mu.Lock()
	t.subs = append(t.subs, entry)
	t.mu.Unlock()
	ch := entry.ch
	return ch, func() {
		entry.once.Do(func() {
			entry.mu.Lock()
			entry.closed = true
			close(entry.ch)
			entry.mu.Unlock()
		})
		t.mu.Lock()
		defer t.mu.Unlock()
		for i, c := range t.subs {
			if c.ch == ch {
				t.subs = append(t.subs[:i], t.subs[i+1:]...)
				break
			}
		}
	}
}

// Publish 发布消息到所有订阅 channel；channel 满时丢旧保新。
// A4：send 前持 subEntry.mu 检查 closed（与 cancel 的 close 同步，race-clean）。
func (t *Topic[T]) Publish(v T) {
	t.mu.Lock()
	subs := append([]*subEntry[T](nil), t.subs...)
	t.mu.Unlock()
	for _, se := range subs {
		se.mu.Lock()
		if se.closed {
			se.mu.Unlock()
			continue
		}
		select {
		case se.ch <- v:
		default:
			select {
			case <-se.ch:
			default:
			}
			select {
			case se.ch <- v:
			default:
			}
		}
		se.mu.Unlock()
	}
}
