// 飞书事件订阅 webhook：URL 验证（challenge 回显）+ im.message.receive_v1
// 提取 chat_id/open_id 落库。纯提取无动作（收到消息=绑定推送目标，不执行指令）。
// 扩展：持有 *FeishuClient + replyHandler，已绑定→解析回复指令→SendText 发确认回执。
// HTTP response 始终返回 {"code":0}（飞书 ACK，不是回复消息）。
package notify

import (
	"context"
	"encoding/json"
	"log"
	"net/http"

	"nox/internal/infra/dephealth"
)

// TargetStore 推送目标存储接口（pgstore 实现，避免 notify→pgstore 依赖）。
type TargetStore interface {
	UpsertFeishuTarget(ctx context.Context, chatID, openID string) error
	FeishuTarget(ctx context.Context) (chatID, openID string, err error)
}

// ReplyEvent 是飞书回复事件的提取结构（D-270）。ParentID D-168：引用回复父消息
// message_id（episode push_msg_id 对位单集 ack）。
type ReplyEvent struct {
	EventID      string
	MessageID    string
	ParentID     string
	ChatID       string
	SenderOpenID string
	MessageType  string
	Content      string
}

// ReplyHandler 处理飞书回复指令（D-270）。
type ReplyHandler interface {
	HandleReply(ctx context.Context, ev ReplyEvent) (string, error) // 返回回复文本（webhook 调 SendText 发送）
}

type feishuEvent struct {
	Type      string `json:"type"` // url_verification=配置验证；event=正常事件
	Challenge string `json:"challenge"`
	Header    struct {
		Token     string `json:"token"`
		EventType string `json:"event_type"`
		EventID   string `json:"event_id"` // D-270：幂等去重
	} `json:"header"`
	Event struct {
		Sender struct {
			SenderID struct {
				OpenID string `json:"open_id"`
			} `json:"sender_id"`
		} `json:"sender"`
		Message struct {
			ChatID      string `json:"chat_id"`
			MessageID   string `json:"message_id"`   // D-270：审计
			ParentID    string `json:"parent_id"`    // D-168：引用回复父消息 id
			MessageType string `json:"message_type"` // D-270："text" 等
			Content     string `json:"content"`      // D-270：JSON 编码内容
		} `json:"message"`
	} `json:"event"`
}

// FeishuWebhookHandler 业主后台事件订阅回调（仅 POST；verifyToken 空则跳过校验）。
// feishu=nil 或 replyHandler=nil → 保持 D-264 只绑定行为（兼容）。
func FeishuWebhookHandler(store TargetStore, verifyToken string, feishu *FeishuClient, replyHandler ReplyHandler) http.Handler {
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
		// im.message.receive_v1 事件处理。
		if ev.Header.EventType == "im.message.receive_v1" && ev.Event.Message.ChatID != "" {
			dephealth.ContactHit("feishu_webhook") // 接触面犬哨：入站命令径命中
			// 查推送目标是否已绑定。
			chatID, _, err := store.FeishuTarget(r.Context())
			if err != nil {
				log.Printf("[feishu] query target: %v", err)
			}
			if chatID == "" {
				// 推送目标未绑定 → 首条消息绑定（保留 D-264 行为）。
				if err := store.UpsertFeishuTarget(r.Context(), ev.Event.Message.ChatID, ev.Event.Sender.SenderID.OpenID); err != nil {
					log.Printf("[feishu] upsert target: %v", err)
				} else {
					log.Printf("[feishu] push target bound: chat_id=%s open_id=%s", ev.Event.Message.ChatID, ev.Event.Sender.SenderID.OpenID)
				}
			} else if replyHandler != nil {
				// 已绑定 → 解析回复指令（D-270）。
				replyEv := ReplyEvent{
					EventID:      ev.Header.EventID,
					MessageID:    ev.Event.Message.MessageID,
					ParentID:     ev.Event.Message.ParentID,
					ChatID:       ev.Event.Message.ChatID,
					SenderOpenID: ev.Event.Sender.SenderID.OpenID,
					MessageType:  ev.Event.Message.MessageType,
					Content:      ev.Event.Message.Content,
				}
				replyText, err := replyHandler.HandleReply(r.Context(), replyEv)
				if err != nil {
					log.Printf("[feishu] reply handler: %v", err)
				} else if replyText != "" && feishu != nil && feishu.Enabled() {
					// 通过 SendText API 发回复（HTTP response 仍返回 {"code":0} ACK）。
					if _, err := feishu.SendText(r.Context(), chatID, replyText); err != nil {
						log.Printf("[feishu] send reply: %v", err)
					}
				}
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
