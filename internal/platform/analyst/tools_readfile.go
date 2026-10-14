package analyst

// tools_readfile.go —— read_doc/read_code 工具件+路径安全（原 tools.go 拆分，零行为变）。

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
)

const maxReadLines = 2000

// execReadFile 读文件（白名单前缀校验 + EvalSymlinks 防符号链接逃逸）。
// allowedSubs 为允许的子目录（相对 KnRoot）。
func (t *Tools) execReadFile(ctx context.Context, call ToolCall, allowedSubs ...string) (ToolResult, []Citation, error) {
	var args struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "参数解析失败: " + err.Error(), IsError: true}, nil, nil
	}
	root := t.KnRoot
	if root == "" {
		root = KnRoot
	}
	full, ok := safePath(root, args.Path, allowedSubs)
	if !ok {
		return ToolResult{ToolUseID: call.ID, Content: "拒绝：路径逃逸或不在白名单内", IsError: true}, nil, nil
	}
	data, err := readFileLines(full, maxReadLines)
	if err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "读取失败: " + err.Error(), IsError: true}, nil, nil
	}
	cite := Citation{Kind: "code", Ref: args.Path, Excerpt: truncate(string(data), 500)}
	kind := "code"
	if len(allowedSubs) == 1 && allowedSubs[0] == "docs" {
		kind = "kb"
	}
	cite.Kind = kind
	return ToolResult{ToolUseID: call.ID, Content: string(data)}, []Citation{cite}, nil
}

// safePath 路径安全校验：filepath.Clean 后前缀校验 + EvalSymlinks 后仍在白名单内。
func safePath(root, rel string, allowedSubs []string) (string, bool) {
	cleanRel := filepath.Clean(rel)
	if filepath.IsAbs(cleanRel) || strings.HasPrefix(cleanRel, "..") {
		return "", false
	}
	full := filepath.Join(root, cleanRel)
	// EvalSymlinks 解析符号链接到真实路径
	real, err := filepath.EvalSymlinks(full)
	if err != nil {
		return "", false
	}
	// 校验真实路径仍在 root 下且在 allowedSubs 之一
	relToRoot, err := filepath.Rel(root, real)
	if err != nil || strings.HasPrefix(relToRoot, "..") {
		return "", false
	}
	for _, sub := range allowedSubs {
		if strings.HasPrefix(relToRoot, sub+string(filepath.Separator)) || relToRoot == sub {
			return real, true
		}
	}
	return "", false
}
