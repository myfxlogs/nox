// report.go 研究纪要落盘（D-513 §5）；收件箱追加已收编 inboxfmt（D-534 三拷贝归一）。
// F2 并发写纪律：与 kbreport/digest 共享收件箱文件——O_APPEND 打开+单次 write，
// 禁读-改-写，防交织丢行。LLM 无 git 权：落盘即止，commit 走席位收工通道。
package explorer

import (
	"fmt"
	"os"
	"path/filepath"
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
