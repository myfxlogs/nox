// proposal_alert_level.go —— 待授权提案露出面票①：提案通告等级判定。
// exec_track=proposal 且非 autoexec=待决策项（needs_ack 语义——进确认
// 生命周期非回执）→warn；autoexec 读败也升 warn（宁多告不漏进 info 轨）。
package oppmatch

import (
	"context"

	"nox/internal/store/pgstore"
)

func proposalAlertLevel(ctx context.Context, st *pgstore.Store, templateID, execTrack string) string {
	if execTrack != "proposal" {
		return "info"
	}
	if ae, err := st.TemplateAutoexec(ctx, templateID); err != nil || !ae {
		return "warn"
	}
	return "info"
}
