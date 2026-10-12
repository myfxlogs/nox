// deliver.go —— D-033：报告三通道投递（docs/报告/ 落档 + alerts 表 + 飞书）。
// 每步失败降级为日志——发送通道故障不丢档/不落空。
package selfwatch

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// deliver 报告投递：①落档 <ReportDir>/selfwatch-<tag>-<BJ日期>.md（同日幂等覆盖，
// 同 sysretro 先例）②InsertAlert(level) 落 alerts 表（StreamUpdates 同步透出）
// ③飞书直推仅 warn+ 级（D-147 推送收口业主域：全绿 info 报告=系统自检常态，
// 留档+留痕不打扰；报告检出异常时调用方传 warn 自动恢复直推）。
func (d Deps) deliver(ctx context.Context, tag, level, body string) {
	dir := d.reportDir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Printf("[selfwatch] report dir: %v", err)
	} else {
		path := filepath.Join(dir, fmt.Sprintf("selfwatch-%s-%s.md", tag, d.now().In(bj).Format("2006-01-02")))
		if werr := os.WriteFile(path, []byte(body), 0o644); werr != nil {
			log.Printf("[selfwatch] report write %s: %v", path, werr)
		}
	}
	if err := d.Store.InsertAlert(ctx, nil, level, body); err != nil {
		log.Printf("[selfwatch] report alert insert: %v", err)
	}
	if level == "info" && tag != "daily" && tag != "weekly" {
		return // D-147：全绿自检报告不推送（业主域=异常/决策/资金变动，info 留痕即足）；
		// D-168 ⑤：daily/weekly 按 tag 放行——D-147 保留清单本钦定 digest 日报要推。
	}
	if d.Notify == nil || !d.Notify.Enabled() || d.ChatID == nil {
		return
	}
	chatID, err := d.ChatID(ctx)
	if err != nil {
		log.Printf("[selfwatch] chat id: %v", err)
		return
	}
	if chatID == "" {
		return
	}
	if _, err := d.Notify.SendText(ctx, chatID, body); err != nil {
		log.Printf("[selfwatch] send %s: %v", tag, err)
	}
}
