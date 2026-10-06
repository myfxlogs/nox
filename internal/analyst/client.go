package analyst

// client.go Anthropic 兼容 messages API 客户端（纯 net/http，无 SDK）。
// POST {ARB_AI_BASE_URL}/v1/messages，SSE 流式解析。baseURL 为根 URL（不含 /v1/messages，代码自动拼接）。
// 设计 §2：x-api-key + anthropic-version: 2023-06-01 头；stream:true。

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client LLM 客户端（Anthropic 兼容 messages API）。
type Client struct {
	BaseURL string
	APIKey  string
	Model   string
	HTTP    *http.Client
}

// NewClient 创建客户端。baseURL 不含尾 /；model 如 "glm-4.6"。
func NewClient(baseURL, apiKey, model string) *Client {
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		APIKey:  apiKey,
		Model:   model,
		HTTP:    &http.Client{Timeout: 120 * time.Second},
	}
}

// MessageRequest messages API 请求体（Anthropic 兼容）。
type MessageRequest struct {
	Model     string     `json:"model"`
	MaxTokens int        `json:"max_tokens"`
	System    string     `json:"system,omitempty"`
	Messages  []Message  `json:"messages"`
	Stream    bool       `json:"stream"`
	Tools     []ToolSpec `json:"tools,omitempty"`
}

// Message 对话消息（user/assistant 角色交替）。
// Content 可以是 string 或 []ContentBlock（工具调用轮次用 block 数组）。
type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// TextContent 纯文本 content（string 形式序列化）。
func TextContent(role, text string) Message {
	b, _ := json.Marshal(text)
	return Message{Role: role, Content: b}
}

// BlocksContent block 数组 content（工具调用轮次）。
func BlocksContent(role string, blocks []ContentBlock) Message {
	b, _ := json.Marshal(blocks)
	return Message{Role: role, Content: b}
}

// ContentBlock content 数组元素（text / tool_use / tool_result）。
type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`
	Name      string          `json:"name,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"` // tool_result 的 content（string 或 block 数组）
	IsError   bool            `json:"is_error,omitempty"`
}

// ToolSpec 工具定义（Anthropic schema 格式）。
type ToolSpec struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

// SSEEvent 解析后的 SSE 事件（type + raw data）。
type SSEEvent struct {
	Type string
	Data json.RawMessage
}

// StreamCallbacks 流式回调（loop.go 注入）。
type StreamCallbacks struct {
	OnDelta    func(text string)       // content_block_delta.text_delta
	OnToolUse  func(call ToolCall)     // content_block_start tool_use + content_block_stop
	OnComplete func(stopReason string) // message_stop / message_delta stop_reason
	OnError    func(err error)         // 任意错误
}

// StreamMessages 发起流式 messages 请求，逐 SSE 事件回调。
// 返回最终 stop_reason（end_turn / tool_use / max_tokens / stop_sequence）。
func (c *Client) StreamMessages(ctx context.Context, req *MessageRequest, cb StreamCallbacks) (string, error) {
	if c.APIKey == "" {
		return "", fmt.Errorf("analyst: ARB_AI_API_KEY 未配置")
	}
	NeutralizeRequest(req) // D-087：发送前载荷中性化（幂等；词表与 walk 规则见 neutralize.go）
	body, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}
	httpReq, err := http.NewRequestWithContext(ctx, "POST", c.BaseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return "", fmt.Errorf("new request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", c.APIKey)
	httpReq.Header.Set("Authorization", "Bearer "+c.APIKey)
	httpReq.Header.Set("anthropic-version", "2023-06-01")
	httpReq.Header.Set("Accept", "text/event-stream")

	resp, err := c.HTTP.Do(httpReq)
	if err != nil {
		return "", fmt.Errorf("do request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("API %d: %s", resp.StatusCode, string(raw))
	}
	return parseSSE(resp.Body, cb)
}

// parseSSE 解析 SSE 流（data: 行 JSON），分发回调。返回最终 stop_reason。
func parseSSE(r io.Reader, cb StreamCallbacks) (string, error) {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	stopReason := "end_turn"
	var pendingTool ToolCall
	var inToolBlock bool
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := strings.TrimPrefix(line, "data: ")
		if data == "[DONE]" {
			break
		}
		var ev struct {
			Type         string          `json:"type"`
			Delta        json.RawMessage `json:"delta"`
			ContentBlock json.RawMessage `json:"content_block"`
			DeltaType    string          `json:"delta.type"`
		}
		if err := json.Unmarshal([]byte(data), &ev); err != nil {
			continue // 跳过非 JSON 行（keepalive 等）
		}
		switch ev.Type {
		case "content_block_start":
			var blk struct {
				Type string `json:"type"`
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(ev.ContentBlock, &blk); err == nil && blk.Type == "tool_use" {
				pendingTool = ToolCall{ID: blk.ID, Name: blk.Name}
				inToolBlock = true
			}
		case "content_block_delta":
			var d struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				PartialJSON string `json:"partial_json"`
			}
			if err := json.Unmarshal(ev.Delta, &d); err == nil {
				if d.Type == "text_delta" && cb.OnDelta != nil {
					cb.OnDelta(d.Text)
				}
				if d.Type == "input_json_delta" && inToolBlock {
					pendingTool.Input += d.PartialJSON
				}
			}
		case "content_block_stop":
			if inToolBlock && cb.OnToolUse != nil {
				cb.OnToolUse(pendingTool)
			}
			inToolBlock = false
			pendingTool = ToolCall{}
		case "message_delta":
			var d struct {
				StopReason string `json:"stop_reason"`
			}
			if err := json.Unmarshal(ev.Delta, &d); err == nil && d.StopReason != "" {
				stopReason = d.StopReason
			}
		case "message_stop":
			if cb.OnComplete != nil {
				cb.OnComplete(stopReason)
			}
		case "error":
			var e struct {
				Error struct {
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal([]byte(data), &e); err == nil && e.Error.Message != "" {
				if cb.OnError != nil {
					cb.OnError(fmt.Errorf("%s", e.Error.Message))
				}
				return stopReason, fmt.Errorf("%s", e.Error.Message)
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return stopReason, fmt.Errorf("sse scan: %w", err)
	}
	return stopReason, nil
}
