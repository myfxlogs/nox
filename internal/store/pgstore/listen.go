// listen.go —— D-004 P1#3：PG LISTEN/NOTIFY 事件原语。
// strategy_stats.disabled 写者含进程外（cmd/resettle、psql 手改），进程内
// bus.Topic 覆盖不到 → 用 DB 触发器 NOTIFY + 本监听原语做全仓唯一同步路。
package pgstore

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5"
)

// listenBackoff 重连退避：1s 起翻倍，30s 封顶（纯函数便于单测封顶序列）。
func listenBackoff(cur time.Duration) time.Duration {
	if cur <= 0 {
		return time.Second
	}
	if cur *= 2; cur > 30*time.Second {
		return 30 * time.Second
	}
	return cur
}

// Listen 在专用连接上 LISTEN channel，每次收到通知调 onEvent()；连接断开自动
// 重连（退避 1s 起翻倍，上限 30s）。每次（重）LISTEN 成功后也调一次 onEvent()——
// 断线期间漏掉的通知由这次全量刷新兜住。阻塞直到 ctx 取消。
// 独立连接不占池位（pool MaxConns 不受影响）；通知可能突发（一次淘汰多个族），
// 不去抖——onEvent 只是一条小查询。
func (s *Store) Listen(ctx context.Context, channel string, onEvent func()) {
	backoff := time.Duration(0)
	for {
		if ctx.Err() != nil {
			return
		}
		conn, err := pgx.ConnectConfig(ctx, s.pool.Config().ConnConfig.Copy())
		if err == nil {
			_, err = conn.Exec(ctx, "LISTEN "+pgx.Identifier{channel}.Sanitize())
		}
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			if conn != nil {
				_ = conn.Close(context.Background())
			}
			backoff = listenBackoff(backoff)
			log.Printf("[pgstore] listen %s: %v (reconnect in %v)", channel, err, backoff)
			if !sleepCtx(ctx, backoff) {
				return
			}
			continue
		}
		backoff = 0
		onEvent() // LISTEN 成功兜底：兜住断线/启动间隙漏掉的通知
		err = waitNotify(ctx, conn, onEvent)
		_ = conn.Close(context.Background())
		if ctx.Err() != nil {
			return
		}
		backoff = listenBackoff(backoff)
		log.Printf("[pgstore] listen %s: %v (reconnect in %v)", channel, err, backoff)
		if !sleepCtx(ctx, backoff) {
			return
		}
	}
}

// waitNotify 阻塞读通知循环；ctx 取消或连接错误时返回。
func waitNotify(ctx context.Context, conn *pgx.Conn, onEvent func()) error {
	for {
		_, err := conn.WaitForNotification(ctx)
		if err != nil {
			return err
		}
		onEvent()
	}
}

// sleepCtx 可中断退避：ctx 取消返 false。
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
