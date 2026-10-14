package analyst

// tools_kb.go —— grep_kb/kb_search 经验库工具件（原 tools.go 拆分，零行为变）。

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"nox/internal/kbsearch"
)

// execGrepKB 递归 grep 限定 /kn 根。旗标用 BusyBox 子集（D-089：alpine 基座无 GNU grep，
// --include 直接 exit 2）——-I 跳二进制、-e 防 pattern 首字符为 -、-l 只列文件；
// .md/.go 限定移到 Go 侧后缀过滤，语义保真（全滤空=无匹配）。
func (t *Tools) execGrepKB(ctx context.Context, call ToolCall) (ToolResult, []Citation, error) {
	var args struct {
		Pattern string `json:"pattern"`
		Path    string `json:"path"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "参数解析失败: " + err.Error(), IsError: true}, nil, nil
	}
	if args.Pattern == "" {
		return ToolResult{ToolUseID: call.ID, Content: "pattern 不能为空", IsError: true}, nil, nil
	}
	root := t.KnRoot
	if root == "" {
		root = KnRoot
	}
	searchRoot := root
	if args.Path != "" {
		full, ok := safePath(root, args.Path, []string{"docs", "internal", "proto", "cmd"})
		if !ok {
			return ToolResult{ToolUseID: call.ID, Content: "拒绝：搜索路径逃逸", IsError: true}, nil, nil
		}
		searchRoot = full
	}
	gctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gctx, "grep", "-rn", "-I", "-l", "-e", args.Pattern, searchRoot)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 1 {
			return ToolResult{ToolUseID: call.ID, Content: "无匹配"}, nil, nil
		}
		return ToolResult{ToolUseID: call.ID, Content: "grep 失败: " + err.Error(), IsError: true}, nil, nil
	}
	// --include 的 .md/.go 限定（D-089）：对输出行按后缀过滤，大小写不敏感。
	var kept []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		low := strings.ToLower(line)
		if strings.HasSuffix(low, ".md") || strings.HasSuffix(low, ".go") {
			kept = append(kept, line)
		}
	}
	files := strings.Join(kept, "\n")
	if files == "" {
		return ToolResult{ToolUseID: call.ID, Content: "无匹配"}, nil, nil
	}
	cite := Citation{Kind: "kb", Ref: truncate(args.Pattern, 100), Excerpt: truncate(files, 500)}
	return ToolResult{ToolUseID: call.ID, Content: files}, []Citation{cite}, nil
}

// execKBSearch 经验库条目对拍（拒单消费闸同源 kbsearch；fail-open——目录读败返
// IsError 行不 panic 主径）。
func (t *Tools) execKBSearch(ctx context.Context, call ToolCall) (ToolResult, []Citation, error) {
	var args struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(call.Input), &args); err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "参数解析失败: " + err.Error(), IsError: true}, nil, nil
	}
	if strings.TrimSpace(args.Text) == "" {
		return ToolResult{ToolUseID: call.ID, Content: "text 不能为空", IsError: true}, nil, nil
	}
	root := t.KnRoot
	if root == "" {
		root = KnRoot
	}
	entries, err := kbsearch.Load(filepath.Join(root, "docs/经验库/条目"))
	if err != nil {
		return ToolResult{ToolUseID: call.ID, Content: "经验库目录读败: " + err.Error(), IsError: true}, nil, nil
	}
	hits := kbsearch.Search(entries, args.Text)
	if len(hits) == 0 {
		return ToolResult{ToolUseID: call.ID, Content: "零命中（高置信门槛内）"}, nil, nil
	}
	if len(hits) > 8 {
		hits = hits[:8]
	}
	var b strings.Builder
	for _, h := range hits {
		fmt.Fprintf(&b, "%s [status=%s hits=%d kw=%s] %s\n", h.ID, h.Status, h.Hits, h.LongKW, h.Summary)
	}
	cite := Citation{Kind: "kb", Ref: "docs/经验库/条目", Excerpt: truncate(b.String(), 500)}
	return ToolResult{ToolUseID: call.ID, Content: b.String()}, []Citation{cite}, nil
}
