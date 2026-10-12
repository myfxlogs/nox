// D-264 测试：token 获取/缓存 + SendText + webhook challenge/事件解析。
package notify

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestFeishuClientTokenAndSend(t *testing.T) {
	tokenCalls, sendCalls := 0, 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/auth/v3/tenant_access_token/internal"):
			tokenCalls++
			w.Write([]byte(`{"code":0,"msg":"ok","tenant_access_token":"t-abc","expire":7200}`))
		case strings.Contains(r.URL.Path, "/im/v1/messages"):
			sendCalls++
			if r.Header.Get("Authorization") != "Bearer t-abc" {
				t.Errorf("auth header = %q, want Bearer t-abc", r.Header.Get("Authorization"))
			}
			if r.URL.Query().Get("receive_id_type") != "chat_id" {
				t.Errorf("receive_id_type = %q", r.URL.Query().Get("receive_id_type"))
			}
			w.Write([]byte(`{"code":0,"msg":"ok"}`))
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	old := feishuBase
	feishuBase = srv.URL
	defer func() { feishuBase = old }()

	c := NewFeishuClient("app", "secret")
	if !c.Enabled() {
		t.Fatal("enabled = false")
	}
	ctx := context.Background()
	if _, err := c.SendText(ctx, "oc_1", "【warn】测试"); err != nil {
		t.Fatalf("SendText: %v", err)
	}
	if _, err := c.SendText(ctx, "oc_1", "again"); err != nil {
		t.Fatalf("SendText cached: %v", err)
	}
	if tokenCalls != 1 {
		t.Errorf("token calls = %d, want 1（第二次走缓存）", tokenCalls)
	}
	if sendCalls != 2 {
		t.Errorf("send calls = %d, want 2", sendCalls)
	}
	// 空 chat_id → 明确错误
	if _, err := c.SendText(ctx, "", "x"); err == nil {
		t.Error("empty chat_id: want error")
	}
	// 禁用态
	if NewFeishuClient("", "").Enabled() {
		t.Error("empty appID: Enabled should be false")
	}
}

type fakeTargetStore struct{ chatID, openID string }

func (f *fakeTargetStore) UpsertFeishuTarget(_ context.Context, chatID, openID string) error {
	f.chatID, f.openID = chatID, openID
	return nil
}

func (f *fakeTargetStore) FeishuTarget(_ context.Context) (chatID, openID string, err error) {
	return f.chatID, f.openID, nil
}

func TestFeishuWebhookHandler(t *testing.T) {
	store := &fakeTargetStore{}
	h := FeishuWebhookHandler(store, "vt-ok", nil, nil)
	// ① challenge 握手
	req := httptest.NewRequest(http.MethodPost, "/webhook/feishu", strings.NewReader(`{"type":"url_verification","challenge":"ch-123"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "ch-123") {
		t.Errorf("challenge: code=%d body=%s", rec.Code, rec.Body.String())
	}
	// ② 事件（token 校验失败 → 401）
	req2 := httptest.NewRequest(http.MethodPost, "/webhook/feishu", strings.NewReader(`{"header":{"token":"wrong","event_type":"im.message.receive_v1"},"event":{"sender":{"sender_id":{"open_id":"ou_me"}},"message":{"chat_id":"oc_p2p"}}}`))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusUnauthorized {
		t.Errorf("bad token: code=%d, want 401", rec2.Code)
	}
	// ③ 事件合法 → 绑定 chat_id/open_id
	req3 := httptest.NewRequest(http.MethodPost, "/webhook/feishu", strings.NewReader(`{"header":{"token":"vt-ok","event_type":"im.message.receive_v1"},"event":{"sender":{"sender_id":{"open_id":"ou_me"}},"message":{"chat_id":"oc_p2p"}}}`))
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	if rec3.Code != 200 {
		t.Fatalf("event: code=%d", rec3.Code)
	}
	if store.chatID != "oc_p2p" || store.openID != "ou_me" {
		t.Errorf("target = %s/%s, want oc_p2p/ou_me", store.chatID, store.openID)
	}
	// ④ 非 POST → 405
	req4 := httptest.NewRequest(http.MethodGet, "/webhook/feishu", nil)
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, req4)
	if rec4.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET: code=%d, want 405", rec4.Code)
	}
}
