// d098_thinking_timeout_test.go —— D-098 锚：Thinking 字段序列化（设置含 disabled /
// 未设字节级零变化）+ 运输层超时上限。
package analyst

import (
	"encoding/json"
	"testing"
	"time"
)

// TestD098_ThinkingSerialization S3：Thinking 设置时请求体含
// `"thinking":{"type":"disabled"}`；未设时请求体与旧形态字节级一致（omitempty 零变化）。
func TestD098_ThinkingSerialization(t *testing.T) {
	base := &MessageRequest{
		Model: "glm-4.6", MaxTokens: 100, System: "s",
		Messages: []Message{TextContent("user", "hi")}, Stream: true,
	}
	legacy, err := json.Marshal(base)
	if err != nil {
		t.Fatal(err)
	}
	if string(legacy) != `{"model":"glm-4.6","max_tokens":100,"system":"s","messages":[{"role":"user","content":"hi"}],"stream":true}` {
		t.Fatalf("未设 thinking 请求体应与旧形态字节级一致: %s", legacy)
	}
	withThink := &MessageRequest{
		Model: "glm-4.6", MaxTokens: 100, System: "s",
		Messages: []Message{TextContent("user", "hi")}, Stream: true,
		Thinking: json.RawMessage(`{"type":"disabled"}`),
	}
	b, err := json.Marshal(withThink)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != `{"model":"glm-4.6","max_tokens":100,"system":"s","messages":[{"role":"user","content":"hi"}],"stream":true,"thinking":{"type":"disabled"}}` {
		t.Fatalf("设置 thinking 应含 disabled 字段: %s", b)
	}
}

// TestD098_TransportTimeoutCeiling S1：运输层上限=600s（explorer 日报整流硬顶根修）。
func TestD098_TransportTimeoutCeiling(t *testing.T) {
	c := NewClient("http://x", "k", "m")
	if c.HTTP.Timeout != 600*time.Second {
		t.Fatalf("HTTP.Timeout = %v, want 600s（D-098 运输层上限）", c.HTTP.Timeout)
	}
}
