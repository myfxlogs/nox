// neutralize_test.go —— D-087 审查对抗中性化测试：映射表长度降序锚、请求形态 walk 锚、幂等锚。
package analyst

import (
	"bytes"
	"encoding/json"
	"testing"
)

// TestNeutralizeText_Mapping 映射表驱动锚：长度降序（持仓盈亏 整词先赢，非 持仓+盈亏 截胡）、
// 数字/符号零触碰、纯英文与空串原样。
func TestNeutralizeText_Mapping(t *testing.T) {
	anchors := []struct{ in, want string }{
		{"持仓盈亏 7 日均值", "position PnL 7 日均值"},
		{"0.01 lot 做空", "0.01 lot go short"},
		{"强制平仓线", "forced liquidation线"},
		{"plain english 123", "plain english 123"},
		{"", ""},
	}
	for _, a := range anchors {
		if got := NeutralizeText(a.in); got != a.want {
			t.Errorf("NeutralizeText(%q) = %q, want %q", a.in, got, a.want)
		}
	}
}

// TestNeutralizeRequest_WalkShapes 请求形态锚：System、user string content、text 块 Text、
// tool_result Content（string 与 []block 两形态）全映射；tool_use 的 Input/ID/Name 逐字节不变。
func TestNeutralizeRequest_WalkShapes(t *testing.T) {
	input := json.RawMessage(`{"symbol":"XAUUSD","side":"做空"}`)
	req := &MessageRequest{
		System: "系统提示：杠杆交易风险",
		Messages: []Message{
			TextContent("user", "帮我评估爆仓风险"),
			{Role: "assistant", Content: json.RawMessage(`[{"type":"tool_use","id":"tu_1","name":"probe","input":` + string(input) + `}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"tu_1","content":"止损位 1234"},{"type":"text","text":"平仓计划"}]`)},
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"tu_2","content":[{"type":"text","text":"强平风险"}]}]`)},
		},
	}
	NeutralizeRequest(req)

	if req.System != "系统提示：leverage交易风险" {
		t.Errorf("System = %q, want %q", req.System, "系统提示：leverage交易风险")
	}
	var s string
	if err := json.Unmarshal(req.Messages[0].Content, &s); err != nil || s != "帮我评估liquidation风险" {
		t.Errorf("user string content = %q (err=%v), want %q", s, err, "帮我评估liquidation风险")
	}

	// tool_use 块：Input/ID/Name 逐字节不变。
	var use []ContentBlock
	if err := json.Unmarshal(req.Messages[1].Content, &use); err != nil {
		t.Fatalf("unmarshal tool_use message: %v", err)
	}
	if string(use[0].Input) != string(input) {
		t.Errorf("Input 被改写: got %s, want %s（工具协议字段禁动）", use[0].Input, input)
	}
	if use[0].ID != "tu_1" || use[0].Name != "probe" {
		t.Errorf("ID/Name 被改写: id=%q name=%q", use[0].ID, use[0].Name)
	}

	// tool_result：Content string 形态 + text 块 Text。
	var res []ContentBlock
	if err := json.Unmarshal(req.Messages[2].Content, &res); err != nil {
		t.Fatalf("unmarshal tool_result message: %v", err)
	}
	if err := json.Unmarshal(res[0].Content, &s); err != nil || s != "stop-loss位 1234" {
		t.Errorf("tool_result Content(string) = %q (err=%v), want %q", s, err, "stop-loss位 1234")
	}
	if res[1].Text != "close position计划" {
		t.Errorf("text 块 Text = %q, want %q", res[1].Text, "close position计划")
	}

	// tool_result：Content []block 形态（嵌套一层 text 块）。
	if err := json.Unmarshal(req.Messages[3].Content, &res); err != nil {
		t.Fatalf("unmarshal nested tool_result message: %v", err)
	}
	var inner []ContentBlock
	if err := json.Unmarshal(res[0].Content, &inner); err != nil || inner[0].Text != "forced liquidation风险" {
		t.Errorf("tool_result Content([]block) text = %q (err=%v), want %q", inner[0].Text, err, "forced liquidation风险")
	}

	// 幂等：二次调用字节不变。
	first, _ := json.Marshal(req)
	NeutralizeRequest(req)
	second, _ := json.Marshal(req)
	if !bytes.Equal(first, second) {
		t.Errorf("NeutralizeRequest 非幂等：二次调用载荷改变")
	}
}

// TestNeutralizeRequest_NoTriggerZeroChange 不含触发词载荷 → 字节级零变换。
func TestNeutralizeRequest_NoTriggerZeroChange(t *testing.T) {
	req := &MessageRequest{
		System: "You are an arb analyst.",
		Messages: []Message{
			TextContent("user", "check ETHUSD funding"),
			{Role: "user", Content: json.RawMessage(`[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]`)},
		},
	}
	before, _ := json.Marshal(req)
	NeutralizeRequest(req)
	after, _ := json.Marshal(req)
	if !bytes.Equal(before, after) {
		t.Errorf("零触发词载荷应字节不变:\nbefore %s\nafter  %s", before, after)
	}
}
