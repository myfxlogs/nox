// kb.go 经验库只读浏览（F8）：解析 docs/经验库/索引.md 表行 + 读条目文件全文。
// 数据源：arb/kb 包 embed 的索引 markdown + 条目目录 embed.FS。
// 安全：条目内容原样返回（前端渲染，不执行任何 markdown 内脚本）；无写路径。
package dashboard

import (
	"context"
	"path"
	"strings"

	kb "arb"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

// parseKBIndex 解析经验库索引 markdown 表行 → KBEntry 列表。
// 表格式：| id | kind | keywords | summary | status | risk |
func parseKBIndex(md string) []*v1.KBEntry {
	lines := strings.Split(md, "\n")
	entries := make([]*v1.KBEntry, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// 跳过非表行（标题、分隔行 |---|、引用 >）
		if !strings.HasPrefix(line, "|") || strings.HasPrefix(line, "|-") || strings.HasPrefix(line, "| id") {
			continue
		}
		// 去首尾 | → split by |
		inner := strings.Trim(line, "|")
		fields := strings.Split(inner, "|")
		if len(fields) < 6 {
			continue
		}
		entries = append(entries, &v1.KBEntry{
			Id:       strings.TrimSpace(fields[0]),
			Kind:     strings.TrimSpace(fields[1]),
			Keywords: strings.TrimSpace(fields[2]),
			Summary:  strings.TrimSpace(fields[3]),
			Status:   strings.TrimSpace(fields[4]),
			Risk:     strings.TrimSpace(fields[5]),
		})
	}
	return entries
}

// ListKBEntries 返回经验库索引全集（只读，F8）。
func (s *Service) ListKBEntries(ctx context.Context, req *connect.Request[v1.ListKBEntriesRequest]) (*connect.Response[v1.ListKBEntriesResponse], error) {
	entries := parseKBIndex(kb.IndexMarkdown)
	return connect.NewResponse(&v1.ListKBEntriesResponse{Entries: entries}), nil
}

// GetKBEntry 返回单条经验条目全文（markdown，含 frontmatter，只读，F8）。
// id 仅允许字母数字与 -（防路径穿越）。
func (s *Service) GetKBEntry(ctx context.Context, req *connect.Request[v1.GetKBEntryRequest]) (*connect.Response[v1.GetKBEntryResponse], error) {
	id := strings.TrimSpace(req.Msg.GetId())
	if id == "" || !isSafeKBID(id) {
		return nil, connect.NewError(connect.CodeInvalidArgument, nil)
	}
	filePath := path.Join("docs/经验库/条目", id+".md")
	data, err := kb.EntriesDir.ReadFile(filePath)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, nil)
	}
	return connect.NewResponse(&v1.GetKBEntryResponse{
		Id:      id,
		Content: string(data),
	}), nil
}

// isSafeKBID 校验 id 仅含 [a-z0-9-]（防路径穿越）。
func isSafeKBID(id string) bool {
	for _, r := range id {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' {
			return false
		}
	}
	return true
}
