// report.go sysretro 公共件：工具白名单/落盘/建议 fenced 解析/收件箱追加。
package sysretro

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"arb/internal/analyst"
)

// retroToolNames 报告专用工具子集（只读白名单，同 kbreport 口径）。
var retroToolNames = map[string]bool{"grep_kb": true, "read_doc": true, "query_ro": true}

// retroToolSpecs 从 analyst.ToolSpecs 过滤只读子集。
func retroToolSpecs() []analyst.ToolSpec {
	var out []analyst.ToolSpec
	for _, s := range analyst.ToolSpecs() {
		if retroToolNames[s.Name] {
			out = append(out, s)
		}
	}
	return out
}

// writeReport 幂等落盘（同日期重跑覆盖同文件）。
func writeReport(dir, name, content string) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("mkdir report dir: %w", err)
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write report: %w", err)
	}
	return path, nil
}

// suggestion 建议行（标题|理由|建议动作）。
type suggestion struct{ title, reason, action string }

// suggestionsRe 建议 fenced 块（```sysretro-suggestions ... ```）。
var suggestionsRe = regexp.MustCompile("(?s)```sysretro-suggestions\\s*\n(.*?)```")

// openFenceRe 未闭合 fenced 块定位（EOF 截断容忍——S5 同 kbreport 修复口径）。
var openFenceRe = regexp.MustCompile("(?s)```sysretro-suggestions\\s*\n(.*)$")

// parseSuggestions 解建议块；EOF 截断（缺闭合 ```）容忍解析并标记 malformed。
func parseSuggestions(aiText string) (out []suggestion, malformed bool) {
	body := ""
	if m := suggestionsRe.FindStringSubmatch(aiText); m != nil {
		body = m[1]
	} else if m := openFenceRe.FindStringSubmatch(aiText); m != nil {
		body, malformed = m[1], true
	} else {
		return nil, false
	}
	for _, ln := range strings.Split(body, "\n") {
		ln = strings.TrimSpace(ln)
		if ln == "" || strings.HasPrefix(ln, "```") {
			continue
		}
		parts := strings.Split(ln, "|")
		title := strings.TrimSpace(parts[0])
		if title == "" {
			continue
		}
		var s suggestion
		s.title = title
		if len(parts) > 1 {
			s.reason = strings.TrimSpace(parts[1])
		}
		if len(parts) > 2 {
			s.action = strings.TrimSpace(parts[2])
		}
		out = append(out, s)
	}
	return out, malformed
}

var inboxMu sync.Mutex

// appendInbox 并发安全追加收件箱（| date | source | title | reason | action | 行）。
func appendInbox(path, date, source string, suggs []suggestion) error {
	var block strings.Builder
	for _, s := range suggs {
		fmt.Fprintf(&block, "| %s | %s | %s | %s | %s |\n",
			date, source, cleanCell(s.title), cleanCell(s.reason), cleanCell(s.action))
	}
	inboxMu.Lock()
	defer inboxMu.Unlock()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.WriteString(block.String())
	return err
}

// cleanCell 落盘表格单元格清洗（同 kbreport cleanCell 口径）。
func cleanCell(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}
