// report.go 收件箱追加+研究纪要落盘（D-513 §5；sysretro report.go 同构）。
// F2 并发写纪律：与 kbreport/digest 共享收件箱文件——O_APPEND 打开+单次 write，
// 禁读-改-写，防交织丢行。LLM 无 git 权：落盘即止，commit 走席位收工通道。
package explorer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

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

var inboxMu sync.Mutex

// appendInbox O_APPEND 单次写追加提案摘要行（| date | source | title | reason | action |）。
func appendInbox(path, date string, rows []inboxRow) error {
	var block strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&block, "| %s | %s | %s | %s | %s |\n",
			date, cleanCell(r.source), cleanCell(r.title), cleanCell(r.reason), cleanCell(r.action))
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

// inboxRow 收件箱一行。
type inboxRow struct{ source, title, reason, action string }

// cleanCell 落盘表格单元格清洗（同 kbreport/sysretro 口径）。
func cleanCell(s string) string {
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.ReplaceAll(s, "|", "\\|")
}
