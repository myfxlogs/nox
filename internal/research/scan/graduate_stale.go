// graduate_stale.go —— pending/unqualified 定期重校验（D-576 双洞修②：
// 快照冻结洞——qualified 只在提案时点跑一次曾致衰减行挂 pending 显幽灵利润）。
// 行现值回跑同一判据 GraduationQualifiedReason（单判据源禁第二径）：
// pending ∧ 不达标 → 'unqualified'（摘牌，decided_note 记拒因留痕）；
// unqualified ∧ 复活达标 → 翻回 'pending'（同行翻转——paper_id UNIQUE 幂等
// 禁新插行，摘牌行须可回牌否则永不再提案）；快照列刷新现值防冻结。
// approved/rejected/stale 终态不碰。
package scan

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/store/pgstore"
)

// revalidatePendingGraduations 返回 (摘牌数, 复活数, err)。
func revalidatePendingGraduations(ctx context.Context, st GraduationStore, now time.Time,
	flags map[string]pgstore.VenueSymbolFlag) (int, int, error) {
	grads, err := st.ListCarryGraduations(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("graduation revalidate: list: %w", err)
	}
	var unpending, revived int
	for _, g := range grads {
		if g.Status != "pending" && g.Status != "unqualified" {
			continue
		}
		p, err := st.GetPaperSpotCarryByID(ctx, g.PaperID)
		if err != nil { // 行读失败→不动作（不明态不误翻转，下轮重试）
			log.Printf("[graduation] revalidate paper#%d: %v", g.PaperID, err)
			continue
		}
		ok, reason := GraduationQualifiedReason(*p, now)
		var target, note string
		switch {
		case g.Status == "pending" && !ok:
			target, note = "unqualified", "摘牌："+reason
		case g.Status == "unqualified" && ok:
			target, note = "pending", "引擎复活回牌（现值达标）"
		default:
			continue
		}
		var net *float64
		if conv := effectiveConvergenceUSD(*p); conv != nil && p.EstRoundTripFeeUSD != nil {
			n := netAllInUSD(p.AccruedUSD, p.EstRoundTripFeeUSD, conv)
			net = &n
		}
		fw := graduationFlagWarn(*p, flags)
		if target == "unqualified" {
			w := "⚠" + note
			fw = &w // 摘牌因透出面板徽标列（D-576 令：flag_warn 记摘牌因）
		}
		if err := st.RevalidateCarryGraduation(ctx, g.ID, target, note,
			p.AccruedUSD, p.LastFundingAnn, net, graduationAgeDays(*p, now), fw); err != nil {
			log.Printf("[graduation] revalidate #%d: %v", g.ID, err) // 单行失败不炸整轮
			continue
		}
		if target == "unqualified" {
			unpending++
		} else {
			revived++
		}
	}
	return unpending, revived, nil
}
