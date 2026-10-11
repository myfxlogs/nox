// feishu_query_dci_test.go —— D-191/D-189：「go」三态口令锚。
package notify

import (
	"context"
	"strings"
	"testing"
)

type fakeGo struct {
	calls    int
	props    []int64
	declines []int64
}

func (f *fakeGo) Go(context.Context) (string, error) { f.calls++; return "申购已提交", nil }
func (f *fakeGo) GoProposal(_ context.Context, id int64) (string, error) {
	f.props = append(f.props, id)
	return "提案申购已提交", nil
}
func (f *fakeGo) DeclineProposal(_ context.Context, id int64) (string, error) {
	f.declines = append(f.declines, id)
	return "已拒批", nil
}

// TestD191_GoCmd：口令形（仅独立 go 词）+未接线回执+接线后调用一次。
func TestD191_GoCmd(t *testing.T) {
	for _, tc := range []struct {
		text            string
		id              int64
		decline, wantOk bool
	}{
		{"go", 0, false, true}, {"  GO ", 0, false, true},
		{"go 12", 12, false, true}, {"go5", 5, false, true},
		{"不go 7", 7, true, true},
		{"go ahead", 0, false, false}, {"gogo", 0, false, false},
		{"不go", 0, false, false}, {"go -3", 0, false, false},
	} {
		id, dec, ok := parseGoCmd(tc.text)
		if ok != tc.wantOk || (ok && (id != tc.id || dec != tc.decline)) {
			t.Fatalf("%q → id=%d dec=%v ok=%v", tc.text, id, dec, ok)
		}
	}
	h := &FeishuQueryHandler{}
	msg, _ := h.handleGoCmd(context.Background(), 0, false)
	if !strings.Contains(msg, "未接线") {
		t.Fatal("未接线回执缺")
	}
	fg := &fakeGo{}
	h.SetDCIGoRunner(fg)
	msg, err := h.handleGoCmd(context.Background(), 0, false)
	if err != nil || fg.calls != 1 || msg != "申购已提交" {
		t.Fatalf("执行回执错: %q %v calls=%d", msg, err, fg.calls)
	}
	// D-189③：go N → GoProposal、不go N → DeclineProposal。
	if _, err := h.handleGoCmd(context.Background(), 9, false); err != nil || len(fg.props) != 1 || fg.props[0] != 9 {
		t.Fatalf("go N 未绑提案: %v props=%v", err, fg.props)
	}
	if _, err := h.handleGoCmd(context.Background(), 9, true); err != nil || len(fg.declines) != 1 || fg.declines[0] != 9 {
		t.Fatalf("不go N 未走拒批: %v declines=%v", err, fg.declines)
	}
}
