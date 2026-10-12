// carry_universe_spot.go —— D-382 S3：spot_carry 第三结构段自 carry_universe.go 整组移出
// （同包纯搬移零行为变更；check-lines 300 行门禁修法，D-366 先例）。
package scan

import (
	"context"
	"log"
	"time"
)

// appendSpotCarryRows D-350 阶段①：第三结构 spot_carry——同所「买现货+空永续」（正 funding）
// /反向（负 funding），现货对冲腿=虚拟零成本腿：net=|funding_ann|−SPOT_CARRY_COST_PCT。
// 数据源=fin_cex_universe（T1∪T3 TRADING 行；T2 类非加密无现货跳过）。独立行独立排名
// （与 CFD 结构不可直接比 net，mt_venue='(spot)' 标记）。持续性闸=first_seen ≥48h。
// 注：D-345 suspect 门不作用于本结构——CEX funding 是真实可收现金流（非 MT swap 定价伪影），
// 质量防线=持续性闸+纸面 normalization 退出（B2USDT 120.4% 持续 3 天=设计在案真候选）。
func (sc *CarryUniverseScanner) appendSpotCarryRows(ctx context.Context, res *ScanResult) {
	uni, err := sc.store.ListCexUniverse(ctx)
	if err != nil {
		// universe 读取失败降级跳过 spot 段（CFD 行照常返回，D-336 语义不回归）
		log.Printf("[scan] carry universe: list cex universe: %v（spot_carry 段本轮跳过）", err)
		uni = nil
	}
	now := time.Now()
	for _, u := range uni {
		if u.Status != "TRADING" || (u.Tier != "T1" && u.Tier != "T3") {
			continue
		}
		if u.FundingAnn == nil {
			continue
		}
		if now.Sub(u.FirstSeen) < spotCarryMinAge {
			continue // 持续性闸：上市窗瞬态 spike 不入候选
		}
		net := abs(*u.FundingAnn) - SPOT_CARRY_COST_PCT
		if net < PaperOpenNetPct {
			continue // D-350R1 S4：落表 floor=net≥20（低于不落行，观测价值不抵榜单噪音）
		}
		res.Rows = append(res.Rows, UniverseRow{
			Canonical: u.Symbol, MtVenue: "(spot)", CexVenue: u.Venue,
			NetAnnual: net, FundingAnn: *u.FundingAnn, FundingInst: *u.FundingAnn,
			SwapFree: true, Structure: "spot_carry",
		})
	}

}
