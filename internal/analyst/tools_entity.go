package analyst

// tools_entity.go get_entity 工具 + readFileLines 辅助。

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// execGetEntity 按 type 分派：position（含 legs+settlement+verification 行）、opportunity、stat-vkey。
// 服务端组装 JSON，模型不拼 SQL。
func (t *Tools) execGetEntity(ctx context.Context, call ToolCall) (ToolResult, []Citation, error) {
	var args struct {
		Type string `json:"type"`
		ID   string `json:"id"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "参数解析失败: " + err.Error(), IsError: true}, nil, nil
	}
	if t.Store == nil {
		return ToolResult{ToolUseID: call.ID, Content: "get_entity 未配置（store 缺失）", IsError: true}, nil, nil
	}
	switch args.Type {
	case "position":
		return t.getEntityPosition(ctx, call.ID, args.ID)
	case "opportunity":
		return t.getEntityOpportunity(ctx, call.ID, args.ID)
	case "stat-vkey":
		return t.getEntityStatVkey(ctx, call.ID, args.ID)
	default:
		return ToolResult{ToolUseID: call.ID, Content: "未知实体类型: " + args.Type, IsError: true}, nil, nil
	}
}

// getEntityPosition position + legs + settlement + verification 行组装。
func (t *Tools) getEntityPosition(ctx context.Context, callID, id string) (ToolResult, []Citation, error) {
	var posID int64
	if _, err := fmt.Sscanf(id, "%d", &posID); err != nil {
		return ToolResult{ToolUseID: callID, Content: "position id 必须为整数", IsError: true}, nil, nil
	}
	p, err := t.Store.GetPosition(ctx, posID)
	if err != nil {
		return ToolResult{ToolUseID: callID, Content: "查 position 失败: " + err.Error(), IsError: true}, nil, nil
	}
	if p == nil {
		return ToolResult{ToolUseID: callID, Content: "position #" + id + " 不存在"}, nil, nil
	}
	legs, _ := t.Store.ListPositionLegs(ctx, posID)
	settle, _ := t.Store.GetSettlementByPos(ctx, posID)
	entity := map[string]any{"position": p, "legs": legs}
	if settle != nil {
		entity["settlement"] = settle
	}
	out, _ := json.Marshal(entity)
	cite := Citation{Kind: "entity", Ref: "position:" + id, Excerpt: truncate(string(out), 500)}
	return ToolResult{ToolUseID: callID, Content: string(out)}, []Citation{cite}, nil
}

// getEntityOpportunity opportunity 单行。
func (t *Tools) getEntityOpportunity(ctx context.Context, callID, id string) (ToolResult, []Citation, error) {
	o, err := t.Store.GetOpportunity(ctx, id)
	if err != nil {
		return ToolResult{ToolUseID: callID, Content: "查 opportunity 失败: " + err.Error(), IsError: true}, nil, nil
	}
	if o == nil {
		return ToolResult{ToolUseID: callID, Content: "opportunity " + id + " 不存在"}, nil, nil
	}
	out, _ := json.Marshal(o)
	cite := Citation{Kind: "entity", Ref: "opportunity:" + id, Excerpt: truncate(string(out), 500)}
	return ToolResult{ToolUseID: callID, Content: string(out)}, []Citation{cite}, nil
}

// getEntityStatVkey verification_stats 按 vkey 查（遍历 ListVerificationStats 匹配）。
func (t *Tools) getEntityStatVkey(ctx context.Context, callID, vkey string) (ToolResult, []Citation, error) {
	stats, err := t.Store.ListVerificationStats(ctx)
	if err != nil {
		return ToolResult{ToolUseID: callID, Content: "查 verification_stats 失败: " + err.Error(), IsError: true}, nil, nil
	}
	for _, s := range stats {
		if s.Vkey == vkey {
			out, _ := json.Marshal(s)
			cite := Citation{Kind: "entity", Ref: "stat-vkey:" + vkey, Excerpt: truncate(string(out), 500)}
			return ToolResult{ToolUseID: callID, Content: string(out)}, []Citation{cite}, nil
		}
	}
	return ToolResult{ToolUseID: callID, Content: "stat-vkey " + vkey + " 不存在"}, nil, nil
}

// readFileLines 读文件前 n 行（防超大文件）。
func readFileLines(path string, maxLines int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	var sb strings.Builder
	n := 0
	for scanner.Scan() {
		sb.WriteString(scanner.Text())
		sb.WriteByte('\n')
		n++
		if n >= maxLines {
			sb.WriteString("... (截断，超过 " + itoa(maxLines) + " 行)\n")
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return []byte(sb.String()), nil
}

func itoa(n int) string { return fmt.Sprintf("%d", n) }
