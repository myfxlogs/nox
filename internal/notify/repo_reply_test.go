// D-270 S3 测试：飞书回复解析+确认处理（fake store 注入，纯逻辑测试）。
package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// fakeReplyStore 注入测试数据，记录调用。
type fakeReplyStore struct {
	chatID               string
	openID               string
	pending              *RepoOpRow
	confirmOK            bool
	confirmSkip          bool
	actionsAdded         bool
	confirmErr           error
	confirmEventID       string
	confirmMessageID     string
	confirmSkipEventID   string
	confirmSkipMessageID string
}

func (f *fakeReplyStore) FeishuTarget(_ context.Context) (chatID, openID string, err error) {
	return f.chatID, f.openID, nil
}
func (f *fakeReplyStore) GetPendingRepoOp(_ context.Context, week string) (*RepoOpRow, error) {
	return f.pending, nil
}
func (f *fakeReplyStore) ConfirmRepoOpOperated(_ context.Context, id int64, amountCny, ratePct float64, operatedDate time.Time, eventID, messageID, via string) (bool, error) {
	if f.confirmErr != nil {
		return false, f.confirmErr
	}
	if f.confirmOK {
		return false, nil // 已处理（幂等）
	}
	f.confirmOK = true
	f.confirmEventID = eventID
	f.confirmMessageID = messageID
	return true, nil
}
func (f *fakeReplyStore) ConfirmRepoOpSkipped(_ context.Context, id int64, reason, eventID, messageID, via string) (bool, error) {
	if f.confirmErr != nil {
		return false, f.confirmErr
	}
	if f.confirmSkip {
		return false, nil // 已处理（幂等）
	}
	f.confirmSkip = true
	f.confirmSkipEventID = eventID
	f.confirmSkipMessageID = messageID
	return true, nil
}
func (f *fakeReplyStore) AddCNFIAction(_ context.Context, code, layer, actionType string, qty, amountCny float64, navAtAction, ratePct *float64, signalSnapshot, rationale, actionDate string) (int64, error) {
	f.actionsAdded = true
	return 1, nil
}

// newReplyHandler 构造测试用回复处理器。
func newReplyHandler(t *testing.T, store *fakeReplyStore) *RepoReplyHandler {
	t.Helper()
	loc, _ := time.LoadLocation("Asia/Shanghai")
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	h := NewRepoReplyHandler(store)
	h.SetNowFn(func() time.Time { return time.Date(2026, 9, 10, 10, 0, 0, 0, loc) })
	return h
}

// boundStore 构造已绑定的 fake store。
func boundStore() *fakeReplyStore {
	return &fakeReplyStore{
		chatID:  "oc_bound",
		openID:  "ou_owner",
		pending: &RepoOpRow{ID: 1, Status: "pending", ReminderCount: 1},
	}
}

// textContent 构造飞书文本消息 content JSON。
func textContent(text string) string {
	return `{"text":"` + text + `"}`
}

// TestReplyParse_OK `OK GC001 50000 2.85` → 解析正确 + cnfi_repo_ops + cnfi_actions 各一行。
func TestReplyParse_OK(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 50000 2.85"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !store.confirmOK {
		t.Error("ConfirmRepoOpOperated not called")
	}
	if !store.actionsAdded {
		t.Error("AddCNFIAction not called (operated should write cnfi_actions)")
	}
	if !strings.Contains(reply, "已记录") {
		t.Errorf("reply = %q, want 含 '已记录'", reply)
	}
}

// TestReplyParse_SKIP `SKIP 周末不在家` → cnfi_repo_ops status=skipped，无 cnfi_actions。
func TestReplyParse_SKIP(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("SKIP 周末不在家"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !store.confirmSkip {
		t.Error("ConfirmRepoOpSkipped not called")
	}
	if store.actionsAdded {
		t.Error("AddCNFIAction called on SKIP, want no actions")
	}
	if !strings.Contains(reply, "已记录跳过") {
		t.Errorf("reply = %q, want 含 '已记录跳过'", reply)
	}
}

// TestReplyParse_Malformed `HELLO` → 回复无法识别。
func TestReplyParse_Malformed(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("HELLO"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "无法识别") {
		t.Errorf("reply = %q, want 含 '无法识别'", reply)
	}
	if store.confirmOK || store.confirmSkip {
		t.Error("confirm called on malformed input")
	}
}

// TestReplyParse_CaseInsensitive `ok gc001 50000 2.85` → 解析正确。
func TestReplyParse_CaseInsensitive(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("ok gc001 50000 2.85"),
	}
	_, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !store.confirmOK {
		t.Error("ConfirmRepoOpOperated not called (case insensitive)")
	}
}

// TestReplyIdempotent 同一 event_id 两次 → ConfirmRepoOpOperated 第二次返回 false。
func TestReplyIdempotent(t *testing.T) {
	store := boundStore()
	store.confirmOK = true // 模拟已处理
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 50000 2.85"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "已处理") {
		t.Errorf("reply = %q, want 含 '已处理' (幂等)", reply)
	}
	if store.actionsAdded {
		t.Error("AddCNFIAction called on idempotent rejection, want no actions")
	}
}

// TestReplyUnauthorizedSender open_id ≠ 绑定 → 回复非授权。
func TestReplyUnauthorizedSender(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_intruder",
		MessageType: "text", Content: textContent("OK GC001 50000 2.85"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "非授权发送者") {
		t.Errorf("reply = %q, want 含 '非授权发送者'", reply)
	}
}

// TestReplyWrongChat chat_id ≠ 绑定 → 回复非授权。
func TestReplyWrongChat(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_wrong", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 50000 2.85"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "非授权 chat") {
		t.Errorf("reply = %q, want 含 '非授权 chat'", reply)
	}
}

// TestReplyNoPendingOpportunity 无 pending → 回复无待确认。
func TestReplyNoPendingOpportunity(t *testing.T) {
	store := boundStore()
	store.pending = nil
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 50000 2.85"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "本周无待确认操作") {
		t.Errorf("reply = %q, want 含 '本周无待确认操作'", reply)
	}
}

// TestReplyOperated_WritesActions operated → cnfi_actions 有 rate_pct。
func TestReplyOperated_WritesActions(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 50000 2.85"),
	}
	_, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !store.actionsAdded {
		t.Error("AddCNFIAction not called (operated must write actions)")
	}
}

// TestReplySkipped_NoActions skipped → 无 cnfi_actions。
func TestReplySkipped_NoActions(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("SKIP 周末不在家"),
	}
	_, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if store.actionsAdded {
		t.Error("AddCNFIAction called on SKIP, want no actions")
	}
}

// TestReplyInvalidAmount `OK GC001 0 2.85` → 回复金额必须为正。
func TestReplyInvalidAmount(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 0 2.85"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "金额和利率必须为正数") {
		t.Errorf("reply = %q, want 含 '金额和利率必须为正数'", reply)
	}
}

// TestReplyInvalidRate `OK GC001 50000 0` → 回复利率必须为正。
func TestReplyInvalidRate(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 50000 0"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "金额和利率必须为正数") {
		t.Errorf("reply = %q, want 含 '金额和利率必须为正数'", reply)
	}
}

// TestReplyNonText message_type="image" → 回复仅支持文本。
func TestReplyNonText(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "image", Content: `{"image_key":"img_abc"}`,
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "仅支持文本指令") {
		t.Errorf("reply = %q, want 含 '仅支持文本指令'", reply)
	}
}

// TestReplyFeishuDisabled FeishuClient.Enabled()=false → 只写 DB，不发飞书确认（webhook 层处理，handler 只返回文本）。
func TestReplyFeishuDisabled(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 50000 2.85"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	// handler 只返回文本，webhook 层判断 feishu.Enabled() 决定是否发送
	if !store.confirmOK {
		t.Error("ConfirmRepoOpOperated not called (should write DB regardless of feishu)")
	}
	if reply == "" {
		t.Error("reply empty, want confirmation text")
	}
}

// TestReplyUnknownInstrument 未知品种 → 回复未知品种。
func TestReplyUnknownInstrument(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC999 50000 2.85"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "未知品种") {
		t.Errorf("reply = %q, want 含 '未知品种'", reply)
	}
}

// TestReplyConfirmError ConfirmRepoOpOperated 失败 → 回复系统错误。
func TestReplyConfirmError(t *testing.T) {
	store := boundStore()
	store.confirmErr = errors.New("DB error")
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 50000 2.85"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err == nil {
		t.Error("HandleReply = nil, want error from ConfirmRepoOpOperated failure")
	}
	if !strings.Contains(reply, "系统错误") {
		t.Errorf("reply = %q, want 含 '系统错误'", reply)
	}
}

// TestReplySkipNoReason `SKIP` 无原因 → 回复需提供原因。
func TestReplySkipNoReason(t *testing.T) {
	store := boundStore()
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-test", MessageID: "msg-test",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("SKIP"),
	}
	reply, err := h.HandleReply(context.Background(), ev)
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "SKIP 需提供原因") {
		t.Errorf("reply = %q, want 含 'SKIP 需提供原因'", reply)
	}
}

// TestReplyOK_EventIDMessageIDPassed 确认 handleOK 将 ev.EventID/ev.MessageID 传给 ConfirmRepoOpOperated。
func TestReplyOK_EventIDMessageIDPassed(t *testing.T) {
	store := &fakeReplyStore{
		chatID: "oc_bound", openID: "ou_owner",
		pending: &RepoOpRow{ID: 1, Status: "pending"},
	}
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-ok-pass", MessageID: "msg-ok-pass",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("OK GC001 50000 2.85"),
	}
	if _, err := h.HandleReply(context.Background(), ev); err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if store.confirmEventID != "evt-ok-pass" {
		t.Errorf("confirmEventID = %q, want evt-ok-pass", store.confirmEventID)
	}
	if store.confirmMessageID != "msg-ok-pass" {
		t.Errorf("confirmMessageID = %q, want msg-ok-pass", store.confirmMessageID)
	}
}

// TestReplySKIP_EventIDMessageIDPassed 确认 handleSKIP 将 ev.EventID/ev.MessageID 传给 ConfirmRepoOpSkipped。
func TestReplySKIP_EventIDMessageIDPassed(t *testing.T) {
	store := &fakeReplyStore{
		chatID: "oc_bound", openID: "ou_owner",
		pending: &RepoOpRow{ID: 1, Status: "pending"},
	}
	h := newReplyHandler(t, store)
	ev := ReplyEvent{
		EventID: "evt-skip-pass", MessageID: "msg-skip-pass",
		ChatID: "oc_bound", SenderOpenID: "ou_owner",
		MessageType: "text", Content: textContent("SKIP 周末不在家"),
	}
	if _, err := h.HandleReply(context.Background(), ev); err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if store.confirmSkipEventID != "evt-skip-pass" {
		t.Errorf("confirmSkipEventID = %q, want evt-skip-pass", store.confirmSkipEventID)
	}
	if store.confirmSkipMessageID != "msg-skip-pass" {
		t.Errorf("confirmSkipMessageID = %q, want msg-skip-pass", store.confirmSkipMessageID)
	}
}
