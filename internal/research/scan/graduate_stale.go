// graduate_stale.go D-474b S3：pending 建议自动过期——每轮扫描回查 paper 行
// 当前态，不再 qualified → status='stale'（终态；快照留档不参与放行）。
// 从 graduate.go 拆出保 300 行门禁。
package scan

import (
	"context"
	"fmt"
	"log"
	"time"
)

// stalePendingGraduations pending 建议逐条回查 paper 行当前态，
// 不再 qualified → status='stale'（终态；快照留作历史不参与放行）。返回过期数。
func stalePendingGraduations(ctx context.Context, st GraduationStore, now time.Time) (int, error) {
	grads, err := st.ListCarryGraduations(ctx)
	if err != nil {
		return 0, fmt.Errorf("graduation stale: list: %w", err)
	}
	staleN := 0
	for _, g := range grads {
		if g.Status != "pending" {
			continue
		}
		p, err := st.GetPaperSpotCarryByID(ctx, g.PaperID)
		if err != nil { // 行读失败→不动作（不明态不误过期，下轮重试）
			log.Printf("[graduation] stale check paper#%d: %v", g.PaperID, err)
			continue
		}
		if graduationQualified(*p, now) {
			continue
		}
		if err := st.UpdateCarryGraduationDecision(ctx, g.ID, "stale", nil,
			"自动过期：行状态不再满足毕业判据"); err != nil {
			log.Printf("[graduation] stale #%d: %v", g.ID, err) // 单行失败不炸整轮
			continue
		}
		staleN++
	}
	return staleN, nil
}
