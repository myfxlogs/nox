// push.go —— 公司日报扇出：alerts info 留痕（看板告警面板可见）+ 飞书直推
// （复用 macro 日历日报形态；Notify/ChatID 任一缺=只留痕不推，fail-open 两级）。
package deptreport

import (
	"context"
	"log"
	"time"
)

// push 公司日报扇出（Generate 末段；单点失败 fail-open）。
func (d *Deps) push(ctx context.Context, date time.Time, body string) {
	text := "【NOX 公司日报 " + date.Format("01-02") + "】\n" + body
	if err := d.Store.InsertAlertDedup(ctx, "info", text, 4*time.Hour); err != nil {
		log.Printf("[deptreport] alert: %v", err)
	}
	if d.Notify == nil || !d.Notify.Enabled() || d.ChatID == nil {
		return
	}
	chatID, err := d.ChatID(ctx)
	if err != nil {
		log.Printf("[deptreport] chat id: %v", err)
		return
	}
	if chatID == "" {
		return
	}
	if _, err := d.Notify.SendText(ctx, chatID, text); err != nil {
		log.Printf("[deptreport] feishu push: %v", err)
	}
}
