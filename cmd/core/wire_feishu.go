// wire_feishu.go —— D-508 S5：feishu 告警扇出 worker 池。
// buf=64 信道 + 4 worker：hook 挂在 InsertAlert 同步链上，发送绝不阻塞落库
// （满则丢弃留痕）；chatID 查询 60s 缓存（告警风暴期免每发一查）。
package main

import (
	"context"
	"log"
	"sync"
	"time"

	"nox/internal/ir/notify"
	"nox/internal/platform/safego"
	"nox/internal/store/pgstore"
)

// wireFeishuPush 返回告警 hook 推送函数（非阻塞投递 jobs 信道）。
func wireFeishuPush(st *pgstore.Store, feishu *notify.FeishuClient) func(pgstore.Alert) {
	jobs := make(chan pgstore.Alert, 64)
	var chatMu sync.Mutex
	var chatID string
	var chatAt time.Time
	chat := func(ctx context.Context) string {
		chatMu.Lock()
		defer chatMu.Unlock()
		if time.Since(chatAt) < 60*time.Second {
			return chatID
		}
		if id, err := st.FeishuChatID(ctx); err == nil {
			chatID, chatAt = id, time.Now()
		}
		return chatID
	}
	for i := 0; i < 4; i++ {
		safego.GoLoop("main:feishu-push", func() {
			for a := range jobs {
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				// D-168 ②：episode 闸——load-modify-write 由 pgstore 内锁序列化，
				// 4 worker 并发安全；fail-open：闸读失败照推（同 dedup 先例不引新丢告警面）。
				act, epKey, gerr := st.AlertEpisodeGate(ctx, a.Level, a.Message, time.Now())
				if gerr != nil {
					log.Printf("[feishu] episode gate fail-open: %v", gerr)
				} else if act == pgstore.EpisodeSuppress {
					cancel()
					continue
				}
				if id := chat(ctx); id != "" {
					text := "【" + levelCN(a.Level) + "】" + a.Message
					if act == pgstore.EpisodeRemind {
						text = "【" + levelCN(a.Level) + "】「告警超时未确认·重推」" + a.Message
					}
					msgID, err := feishu.SendText(ctx, id, text)
					if err != nil {
						log.Printf("[feishu] push failed: %v", err)
					} else if epKey != "" && msgID != "" {
						if rerr := st.RecordAlertEpisodePushMsg(ctx, epKey, msgID); rerr != nil {
							log.Printf("[feishu] episode push_msg record: %v", rerr)
						}
					}
				}
				cancel()
			}
		})
	}
	return func(a pgstore.Alert) {
		if !feishu.Enabled() || a.Level == "info" {
			return
		}
		select {
		case jobs <- a:
		default:
			log.Printf("[feishu] push queue full, drop %s alert", a.Level)
		}
	}
}

// levelCN 告警级别前缀中文化（D-573 S3：纯展示层映射，DB level 枚举与写入值零动；
// 未知值原样透出）。
func levelCN(level string) string {
	switch level {
	case "warn", "warning":
		return "警告"
	case "critical":
		return "严重"
	case "info":
		return "提示"
	case "error":
		return "错误"
	}
	return level
}
