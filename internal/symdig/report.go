// report.go D-464 S4：每轮 diff 摘要落 docs/报告/symdig-YYYY-MM-DD.md（同日追加）。
package symdig

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// writeReport 报告件：分轮追加分节（幂等=run_ts 分节独立，同日多轮共存）。
func writeReport(dir string, runTS time.Time, sum *Summary) (string, error) {
	if dir == "" {
		return "", nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, "symdig-"+runTS.Format("2006-01-02")+".md")
	var b strings.Builder
	if _, err := os.Stat(path); os.IsNotExist(err) {
		fmt.Fprintf(&b, "# 配对池深挖 %s\n\n> 数字=Go 确定性分类；klass 值域 paired/unpaired/disabled/unmapped_unknown/mispaired/unit_unknown（D-594）。\n\n",
			runTS.Format("2006-01-02"))
	}
	fmt.Fprintf(&b, "## 轮次 %s\n\n", runTS.Format("15:04:05 UTC"))
	keys := make([]string, 0, len(sum.ByKlass))
	for k := range sum.ByKlass {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	b.WriteString("```\n")
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %d\n", k, sum.ByKlass[k])
	}
	fmt.Fprintf(&b, "new_unpaired: %d\n", len(sum.NewUnpaired))
	for _, k := range sum.NewUnpaired {
		fmt.Fprintf(&b, "  + %s\n", k)
	}
	b.WriteString("```\n\n")
	// 非 paired/disabled 明细（diff 对象即深挖输入）。
	b.WriteString("### 未配对/异常明细\n\n```\n")
	for _, r := range sum.Rows {
		if r.Klass == "paired" || r.Klass == "disabled" {
			continue
		}
		fmt.Fprintf(&b, "%s %s %s %s\n", r.Venue, r.Symbol, r.Klass, r.Reason)
	}
	b.WriteString("```\n\n")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if _, err := f.WriteString(b.String()); err != nil {
		return "", err
	}
	return path, nil
}
