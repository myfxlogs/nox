// D-176② 锚：飞书「批准#N [D-xxx]」「拒绝#N [理由]」口令——
// LLM 前拦截、路由 ProbeDecider（同 RPC 语义）、幂等应答、缺 D# 引导。
package notify

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeDecider struct {
	approveCalls int
	rejectCalls  int
	lastID       int64
	lastRef      string
	lastReason   string
	status       string
	message      string
	posID        int64
	err          error
}

func (f *fakeDecider) ApproveProbe(_ context.Context, id int64, ref string) (string, string, int64, error) {
	f.approveCalls++
	f.lastID, f.lastRef = id, ref
	return f.status, f.message, f.posID, f.err
}

func (f *fakeDecider) RejectProbe(_ context.Context, id int64, reason string) (string, string, error) {
	f.rejectCalls++
	f.lastID, f.lastReason = id, reason
	return f.status, f.message, f.err
}

func d176Handler(t *testing.T, d ProbeDecider) *FeishuQueryHandler {
	t.Helper()
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1"}
	h := newTestHandler(fs, "http://unused", 10*time.Second)
	h.SetProbeDecider(d)
	return h
}

// 批准口令携 D# → 路由 decider + 回执携 pos id。
func TestD176_ApproveCmd(t *testing.T) {
	d := &fakeDecider{status: "RUNNING", posID: 42}
	h := d176Handler(t, d)
	reply, err := h.HandleReply(context.Background(), textEvent("批准#20 D-176"))
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if d.approveCalls != 1 || d.lastID != 20 || d.lastRef != "D-176" {
		t.Fatalf("路由参数错 calls=%d id=%d ref=%q", d.approveCalls, d.lastID, d.lastRef)
	}
	if !strings.Contains(reply, "#20") || !strings.Contains(reply, "pos=42") {
		t.Fatalf("回执应携 id+pos, got %q", reply)
	}
}

// 批准口令缺 D# → 不路由，回引导语。
func TestD176_ApproveCmdNoRef(t *testing.T) {
	d := &fakeDecider{}
	h := d176Handler(t, d)
	reply, err := h.HandleReply(context.Background(), textEvent("批准#20"))
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if d.approveCalls != 0 {
		t.Fatalf("缺 D# 不应路由, calls=%d", d.approveCalls)
	}
	if !strings.Contains(reply, "决策引用") {
		t.Fatalf("应回引导语, got %q", reply)
	}
}

// 拒绝口令无理由 → 缺省理由路由；已裁定幂等应答=RPC 原态回显。
func TestD176_RejectCmdIdempotent(t *testing.T) {
	d := &fakeDecider{status: "running", message: "提案已处 running 态（不可拒）"}
	h := d176Handler(t, d)
	reply, err := h.HandleReply(context.Background(), textEvent("拒绝#20"))
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if d.rejectCalls != 1 || d.lastReason != "飞书口令拒绝" {
		t.Fatalf("缺省理由未路由 calls=%d reason=%q", d.rejectCalls, d.lastReason)
	}
	if !strings.Contains(reply, "running") {
		t.Fatalf("幂等应答应回显原态, got %q", reply)
	}
}

// decider 未装配 → 明确回「未接线」，不静默走 LLM。
func TestD176_NoDecider(t *testing.T) {
	fs := &fakeQueryStore{chatID: "chat1", openID: "user1"}
	h := newTestHandler(fs, "http://unused", 10*time.Second)
	reply, err := h.HandleReply(context.Background(), textEvent("批准#20 D-176"))
	if err != nil {
		t.Fatalf("HandleReply: %v", err)
	}
	if !strings.Contains(reply, "未接线") {
		t.Fatalf("应回未接线, got %q", reply)
	}
}
