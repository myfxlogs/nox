package reconcile

import (
	"context"
	"log"
	"time"
)

// StartLoop 定期对账。阻塞至 ctx 取消。
// 缺陷1修复：initial Run 放独立 goroutine（受 runTimeout 保护，不阻塞 loop）。
// 缺陷2修复：initial 延迟 initialDelay（默认 60s，等 MT4/MT5 adapter 就绪，避免空查证器误报）。
func (r *Reconciler) StartLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// 首轮延迟（等 adapter 就绪），独立 goroutine 跑（不阻塞 ticker loop）
	go func() {
		select {
		case <-ctx.Done():
			return
		case <-time.After(r.initialDelay):
		}
		if _, err := r.Run(ctx); err != nil {
			log.Printf("[reconcile] initial: %v", err)
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := r.Run(ctx); err != nil {
				log.Printf("[reconcile] run: %v", err)
			}
		}
	}
}
