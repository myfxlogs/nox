// D-264 飞书事件订阅 webhook：URL 验证（challenge 回显）+ im.message.receive_v1
// 提取 chat_id/open_id 落库。纯提取无动作（收到消息=绑定推送目标，不执行指令）。
package notify

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
)

// TargetStore 推送目标存储接口（pgstore 实现，避免 notify→pgstore 依赖）。
type TargetStore interface {
	UpsertFeishuTarget(ctx context.Context, chatID, openID string) error
}

type feishuEvent struct {
	Type      string `json:"type"` // url_verification=配置验证；event=正常事件
	Challenge string `json:"challenge"`
	Header    struct {
		Token     string `json:"token"`
		EventType string `json:"event_type"`
	} `json:"header"`
	Event struct {
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
		} `json:"sender"`
		Message struct {
			ChatID string `json:"chat_id"`
		} `json:"message"`
	} `json:"event"`
}

// FeishuWebhookHandler 业主后台事件订阅回调（仅 POST；verifyToken 空则跳过校验）。
func FeishuWebhookHandler(store TargetStore, verifyToken string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var ev feishuEvent
		if err := json.NewDecoder(r.Body).Decode(&ev); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		// URL 验证（后台配置回调地址时的握手）：原样回显 challenge。
		if ev.Type == "url_verification" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"challenge":` + quoteJSON(ev.Challenge) + `}`))
			return
		}
		// 校验（配置了 verify token 才启用）。
		if verifyToken != "" && ev.Header.Token != verifyToken {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		// 首条消息 → 绑定推送目标。
		if ev.Header.EventType == "im.message.receive_v1" && ev.Event.Message.ChatID != "" {
			if err := store.UpsertFeishuTarget(r.Context(), ev.Event.Message.ChatID, ev.Event.Sender.SenderID.OpenID); err != nil {
				log.Printf("[feishu] upsert target: %v", err)
			} else {
				log.Printf("[feishu] push target bound: chat_id=%s open_id=%s", ev.Event.Message.ChatID, ev.Event.Sender.SenderID.OpenID)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0}`))
	})
}

func quoteJSON(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
