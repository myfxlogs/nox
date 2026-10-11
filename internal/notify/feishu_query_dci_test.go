// feishu_query_dci_test.go —— D-191：「go」口令锚。
package notify

import (
	"context"
	"strings"
	"testing"
)

type fakeGo struct{ calls int }

func (f *fakeGo) Go(context.Context) (string, error) { f.calls++; return "申购已提交", nil }

// TestD191_GoCmd：口令形（仅独立 go 词）+未接线回执+接线后调用一次。
func TestD191_GoCmd(t *testing.T) {
	if !isGoCmd("go") || !isGoCmd("  GO ") || isGoCmd("go ahead") || isGoCmd("gogo") {
		t.Fatal("口令形误判")
	}
	h := &FeishuQueryHandler{}
	msg, _ := h.handleGoCmd(context.Background())
	if !strings.Contains(msg, "未接线") {
		t.Fatal("未接线回执缺")
	}
	fg := &fakeGo{}
	h.SetDCIGoRunner(fg)
	msg, err := h.handleGoCmd(context.Background())
	if err != nil || fg.calls != 1 || msg != "申购已提交" {
		t.Fatalf("执行回执错: %q %v calls=%d", msg, err, fg.calls)
	}
}
