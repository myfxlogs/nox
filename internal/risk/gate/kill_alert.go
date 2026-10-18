// kill_alert.go —— kill-switch 触发通知（D-622 泛化二批，control_plane.go ≤300 行
// 棘轮门禁拆出）：kill 触发=最高优席位动作——critical 告警末行嵌席转发令
// （seatDispatch 单点 alerttext.SeatDispatchLine；文案静态不参与 dedup 漂移）。
package gate

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/platform/alerttext"
	"nox/internal/store/pgstore"
)

// alertKillTriggered kill 触发通知——UpsertExecutionControl 成功后落 critical 告警；
// 告警写失败只落 log（kill 已生效，通知失败不反转触发本体）。dedup 窗内同文互抑
// （重触发同窗不重复推送；resolve 后新触发同文案同窗仍抑——窗口自然放行语义）。
func alertKillTriggered(ctx context.Context, store *pgstore.Store, actor, reason string) {
	msg := fmt.Sprintf("kill switch 触发（actor=%s）：%s%s", actor, reason,
		alerttext.SeatDispatchLine("决策", "kill 处置（读 STATE——核触发因+GrantLive 重授判断）"))
	if err := store.InsertAlertDedup(ctx, "critical", msg, time.Hour); err != nil {
		log.Printf("[control-plane] kill trigger alert: %v", err)
	}
}
