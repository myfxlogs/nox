// broker_tz_test.go —— ServerTZCache 并发安全（b952f491 生产实证：
// flush 解锁后并发 dirtyLocked 再挂 AfterFunc + RPC 快照路径同调 get →
// 无锁 map 并发写 fatal。-race 下 20 goroutine 同键/异键并发 get 覆盖）。
package dashboard

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestServerTZCache_ConcurrentGet(t *testing.T) {
	ctx := context.Background()
	c := NewServerTZCache(time.Hour, func(_ context.Context, _ string) (float64, error) {
		time.Sleep(5 * time.Millisecond) // 拉大锁外 fetch 窗口，逼迫竞争重叠
		return 3, nil
	})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			key := "v:mt5"
			if i%2 == 0 {
				key = "v2:mt5" // 同键+异键混合并发
			}
			for j := 0; j < 5; j++ {
				_ = c.get(ctx, key)
			}
		}(i)
	}
	wg.Wait()
}
