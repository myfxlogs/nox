// D-209：broker 首连指数退避重试（boot 路径专用，AddBroker 保持一次性）。
package dashboard

import (
	"context"
	"log"
	"time"
)

// retryBackoff 指数退避：1s,2s,4s...封顶 60s。包级 var 便于测试覆写。
var retryBackoff = func(attempt int) time.Duration {
	d := time.Duration(1<<uint(attempt)) * time.Second
	if d > 60*time.Second {
		return 60 * time.Second
	}
	return d
}

// startWithRetry 包装 startAdapter：失败按指数退避重试直到成功或 ctx 取消。
// boot 路径专用——AddBroker 手动路径保持一次性（需即时错误反馈）。
// startFn 注入以便测试；每次 attempt 打日志（venue/attempt/下次间隔）。
func (m *BrokerManager) startWithRetry(ctx context.Context, venue string, startFn func() error) {
	for attempt := 0; ; attempt++ {
		if err := startFn(); err == nil {
			if attempt > 0 {
				log.Printf("[broker-retry:%s] connected after %d attempts", venue, attempt+1)
			}
			return
		} else {
			wait := retryBackoff(attempt)
			log.Printf("[broker-retry:%s] attempt %d failed: %v, retry in %s", venue, attempt+1, err, wait)
			select {
			case <-ctx.Done():
				log.Printf("[broker-retry:%s] cancelled during backoff", venue)
				return
			case <-time.After(wait):
			}
		}
	}
}
