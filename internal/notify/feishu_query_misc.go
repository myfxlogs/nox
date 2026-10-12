// feishu_query_misc.go —— 今日收益+文本提取工具（自 feishu_query.go 拆分，纯移动零语义改）。
package notify

import (
	"context"
	"encoding/json"
	"fmt"

	"arb/internal/analyst"
)

func (t *feishuQueryTools) execTodayIncome(ctx context.Context, call analyst.ToolCall) (analyst.ToolResult, []analyst.Citation, error) {
	energy, voting, interest, err := t.store.SumTodayIncome(ctx)
	if err != nil {
		return analyst.ToolResult{ToolUseID: call.ID, Content: "查今日收益失败: " + err.Error(), IsError: true}, nil, nil
	}
	out, _ := json.Marshal(map[string]any{
		"energy_trx":   energy,
		"voting_trx":   voting,
		"interest_usd": interest,
	})
	return analyst.ToolResult{ToolUseID: call.ID, Content: string(out)}, nil, nil
}

// extractText 从飞书 content JSON 提取 text 字段。
// D-139：原 repo_reply.go 定义随 repo 面摘除，挪此（本文件唯一在用方）。
func extractText(content string) (string, error) {
	if content == "" {
		return "", fmt.Errorf("empty content")
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(content), &m); err != nil {
		return "", err
	}
	return m["text"], nil
}
