// D-168 ④：飞书 ack 拦截测试锚——裸词全 ack、parent_id 单集、非 ack 词走 LLM、
// SendText 返 message_id、webhook parent_id 提取。
package notify

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func d168Ev(text, parentID string) ReplyEvent {
	content, _ := json.Marshal(map[string]string{"text": text})
	return ReplyEvent{ChatID: "chat1", SenderOpenID: "user1", ParentID: parentID,
		MessageType: "text", Content: string(content)}
}

// 裸词「收到」→ ack 全部 open episode + alerts.AckAll 同步 + 回执条数。
func TestD168_AckBareAll(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1", epsAckN: 3}
	h := newTestHandler(fs, "http://unused", 10*time.Second)
	reply, err := h.HandleReply(context.Background(), d168Ev("收到", ""))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if !fs.ackAllCalled || !fs.alertAckAll {
		t.Fatalf("ack paths: episodes=%v alerts=%v", fs.ackAllCalled, fs.alertAckAll)
	}
	if !strings.Contains(reply, "3") {
		t.Errorf("reply=%q, want contains 3", reply)
	}
}

// 引用回复（parent_id）→ 只 ack 对应集 + 按键同步 alerts。
func TestD168_AckByParentID(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1",
		epByMsg: map[string]string{"om_42": "warn:key42"}, alertAckN: 2}
	h := newTestHandler(fs, "http://unused", 10*time.Second)
	reply, err := h.HandleReply(context.Background(), d168Ev("知道了", "om_42"))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if fs.ackAllCalled {
		t.Fatal("bare-ack path wrongly taken")
	}
	if fs.ackByKeySeen != "warn:key42" {
		t.Errorf("ackByKeySeen=%q, want warn:key42", fs.ackByKeySeen)
	}
	if !strings.Contains(reply, "已确认该告警集") {
		t.Errorf("reply=%q, want contains 已确认该告警集", reply)
	}
}

// parent_id 对位不到 → 明确回执，不误 ack 全部。
func TestD168_AckParentMiss(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1", epByMsg: map[string]string{}}
	h := newTestHandler(fs, "http://unused", 10*time.Second)
	reply, err := h.HandleReply(context.Background(), d168Ev("收到", "om_404"))
	if err != nil {
		t.Fatalf("err=%v", err)
	}
	if fs.ackAllCalled {
		t.Fatal("miss 不应升级为全 ack")
	}
	if !strings.Contains(reply, "未找到") {
		t.Errorf("reply=%q, want contains 未找到", reply)
	}
}

// 非 ack 词（含 ack 子串的查询句）→ LLM 径原样。
func TestD168_NonAckGoesLLM(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1", cmcPrice: 0.33, cmcPriceTs: time.Now()}
	feishuSrv, msgCh := feishuMockServer(t)
	old := feishuBase
	feishuBase = feishuSrv.URL
	t.Cleanup(func() { feishuBase = old })
	llmSrv := llmToolTextServer(t, "query_trx_price", "0.33")
	h := newTestHandler(fs, llmSrv.URL, 10*time.Second)
	reply, err := h.HandleReply(context.Background(), d168Ev("收到了吗，TRX多少钱", ""))
	if err != nil || reply != "" {
		t.Fatalf("reply=%q err=%v（应异步走 LLM）", reply, err)
	}
	if fs.ackAllCalled {
		t.Fatal("非裸词不应触发 ack")
	}
	waitForMsg(t, msgCh)
}

// SendText 返回解析的 message_id。
func TestD168_SendTextReturnsMsgID(t *testing.T) {
	feishuSrv, _ := feishuMockServer(t)
	old := feishuBase
	feishuBase = feishuSrv.URL
	t.Cleanup(func() { feishuBase = old })
	c := NewFeishuClient("app", "secret")
	mid, err := c.SendText(context.Background(), "chat1", "hi")
	if err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if mid != "om_test_1" {
		t.Errorf("mid=%q, want om_test_1", mid)
	}
}

// webhook 事件提取 parent_id。
func TestD168_WebhookParentID(t *testing.T) {
	var got ReplyEvent
	h := FeishuWebhookHandler(&fakeTarget{chatID: "chat1"}, "", nil, replyFunc(func(_ context.Context, ev ReplyEvent) (string, error) {
		got = ev
		return "", nil
	}))
	body := `{"header":{"event_type":"im.message.receive_v1","event_id":"e1"},
		"event":{"sender":{"sender_id":{"open_id":"u1"}},
		"message":{"chat_id":"chat1","message_id":"om_m1","parent_id":"om_p1","message_type":"text","content":"{\"text\":\"收到\"}"}}}`
	req := httptest.NewRequest(http.MethodPost, "/webhook/feishu", strings.NewReader(body))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	if got.ParentID != "om_p1" {
		t.Errorf("ParentID=%q, want om_p1", got.ParentID)
	}
}

// fakeTarget/replyFunc webhook 测试件。
type fakeTarget struct{ chatID string }

func (f *fakeTarget) UpsertFeishuTarget(context.Context, string, string) error { return nil }
func (f *fakeTarget) FeishuTarget(context.Context) (string, string, error) {
	return f.chatID, "u1", nil
}

type replyFunc func(context.Context, ReplyEvent) (string, error)

func (f replyFunc) HandleReply(ctx context.Context, ev ReplyEvent) (string, error) {
	return f(ctx, ev)
}
