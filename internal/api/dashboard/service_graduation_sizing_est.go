// service_graduation_sizing_est.go —— D-490 S2 拆分件（棘轮消化）：
// sizing 回显/池行/估值填充三件从 service_graduation_sizing.go 纯搬移，
// 零行为变（函数签名/import 归类沿用同包惯例）。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"

	v1 "nox/internal/gen/dashboardv1"
)

func (s *Service) gradLiveLegPools(ctx context.Context, snaps []pgstore.VenueAccountLatest, legKeys []string) (rows []*v1.GradLegPoolRow, poolB allocPool, okB bool, poolS allocPool, okS bool) {
	pools := make([]allocPool, len(legKeys))
	oks := make([]bool, len(legKeys))
	for i, vk := range legKeys {
		pools[i], oks[i] = s.legPool(ctx, snaps, vk)
	}
	rows = gradLegPoolsOf(legKeys, pools, oks)
	if len(pools) > 0 {
		poolB, okB = pools[0], oks[0]
	}
	if len(pools) > 1 {
		poolS, okS = pools[1], oks[1]
	}
	return rows, poolB, okB, poolS, okS
}

// poolSrcNote 池来源注记（sizing_notes 透出——实拉/校准 vs 快照龄）。
func poolSrcNote(p allocPool) string {
	if p.live {
		if p.src == "rest" {
			return fmt.Sprintf("实拉校准 %d 秒前", int(time.Since(p.at).Seconds()))
		}
		return "实拉"
	}
	return fmt.Sprintf("快照 %d 秒前（快照兜底）", int(time.Since(p.at).Seconds()))
}

// allocNoteMarker D-490 §4：decided_note 审计标记——pct 侧「分配$X(=P%×可用基准$Ntype)」、
// usd 侧「分配$X(直填=P%×可用基准$N)」；业主 note 保留在前（本函数返回拼接后整串）。
// D-540：基准=可用资金（free_margin 优先缺省回退净值），标记同口径不残留净值字样。
func allocNoteMarker(userNote string, res allocSizing, pctInput bool) string {
	poolType := res.AllocBaseKey
	if t := keydomain.Parse(poolType).Type; t != "" {
		poolType = t
	}
	marker := fmt.Sprintf("分配$%.1f(直填=%.1f%%×可用基准$%.0f)", res.AllocUSD, res.AllocPct*100, res.AllocBaseUSD)
	if pctInput {
		marker = fmt.Sprintf("分配$%.1f(=%.1f%%×可用基准$%.0f%s)", res.AllocUSD, res.AllocPct*100, res.AllocBaseUSD, poolType)
	}
	return strings.TrimSpace(userNote + " " + marker)
}

// fillSizingEst D-490：折算结果回显 proto 字段（dry_run 试算与真批准同径透出）。
func fillSizingEst(resp *v1.ApproveCarryGraduationResponse, res allocSizing) {
	resp.EstGrossUsd = res.GrossUSD
	resp.EstLegNotionalUsd = res.LegNotional
	resp.EstLongQty = res.LongQty
	resp.EstShortQty = res.ShortQty
	resp.EstAllocUsd = res.AllocUSD
	resp.EstAllocPct = res.AllocPct
	resp.SizingNotes = res.Notes
	// D-540：可用基准+风险位+建议额随试算/批准同径透出。
	resp.AllocBaseUsd = res.AllocBaseUSD
	resp.AllocBaseNote = res.AllocBaseNote
	resp.RiskLevel = res.RiskLevel
	resp.SuggestedAllocUsd = res.SuggestedAlloc
	resp.RiskReasons = res.RiskReasons
}

// fillExplicitEst 显式模式 dry_run 回显（无折算——回显业主将写入的原值；
// alloc 字段留零=无池分配语义）。
func fillExplicitEst(resp *v1.ApproveCarryGraduationResponse, longQty, shortQty, gross float64, notional *float64) {
	resp.EstGrossUsd = gross
	resp.EstLongQty = longQty
	resp.EstShortQty = shortQty
	if notional != nil {
		resp.EstLegNotionalUsd = *notional
	}
}
