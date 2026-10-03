// carry_universe_join.go — D-043：join 键投影与结构计算（自 carry_universe.go 拆出，
// 300 行门禁）。每条腿在 legacy canonical ∪ asset_class 双键下注册，同类即相遇；
// 行 Canonical=相遇键（同 canonical 对仍得 canonical 键——XAU 行不变；跨 canonical
// 对得 class 键——SILVER×XAGUSDT/BRENT×BZUSDT/WTI×CLUSDT 盲区闭环）。
package scan

import (
	"context"
	"time"

	"arb/internal/fact"
)

// joinCfdLegs CEX funding × MT swap 双腿 join + 结构行生成（D-412 F2 全叉积：
// 每 (canonical, mt_venue, cex_venue) 三元组两结构各行；去重取 canonical 键行）。
func (sc *CarryUniverseScanner) joinCfdLegs(ctx context.Context, cexFunding []fact.Fact,
	aliases map[string]string, view *assetClassView,
	longByCanon, shortByCanon map[string]mtSwap, filteredCanonicals map[string]bool, res *ScanResult) {
	type cexF struct {
		venue, canonical, sym string
		val, inst             float64 // val=7d 均值年化（判定全走此值）；inst=瞬时（展示列）
		stale                 bool    // F4：均值不可证/最新样本龄期超闸（>UniverseFundingMaxAge）
	}
	fundByKey := map[string][]cexF{}
	for _, f := range cexFunding {
		canonical := applyAlias(NormalizeJoinKey(cexCanonical(f.Symbol)), aliases) // D-336R1+D-464：归一+别名
		if canonical == "" {
			res.Skipped++
			continue
		}
		mean, mts, mok := fact.FundingMeanAnnual(ctx, sc.store, f.Venue, f.Symbol, fact.FundingMeanWindow)
		cf := cexF{
			venue: f.Venue, canonical: canonical, sym: f.Symbol, val: mean, inst: f.Value,
			stale: !mok || (sc.fundingMaxAge > 0 && time.Since(mts) > sc.fundingMaxAge),
		}
		for _, k := range view.joinKeys(canonical, view.cexClassOf(f.Venue, cexFactSymbol(f.Symbol))) {
			fundByKey[k] = append(fundByKey[k], cf)
		}
	}
	longIdx := map[string]map[string]mtSwap{} // joinKey → venue → 腿（簿仍 canonical 键，此为 join 投影）
	shortIdx := map[string]map[string]mtSwap{}
	addLeg := func(idx map[string]map[string]mtSwap, s mtSwap) {
		for _, k := range view.joinKeys(s.canonical, view.mtClassOf(s.venue, s.rawCanon)) {
			m := idx[k]
			if m == nil {
				m = map[string]mtSwap{}
				idx[k] = m
			}
			m[s.venue] = s
		}
	}
	for _, s := range longByCanon {
		addLeg(longIdx, s)
	}
	for _, s := range shortByCanon {
		addLeg(shortIdx, s)
	}
	// 去重：同一（MT 腿, funding 腿, 结构）可在 canonical 键与 class 键下双遇
	// （XAUUSD×XAUUSDT → XAU 键与 GOLD 键）——取 canonical 键行（旧语义零漂移）。
	type emitCand struct {
		row       UniverseRow
		preferred bool // 键=funding 腿自身 canonical
	}
	emitted := map[string]emitCand{}
	emit := func(row UniverseRow, dk string, preferred bool) {
		if old, dup := emitted[dk]; !dup || (!old.preferred && preferred) {
			emitted[dk] = emitCand{row: row, preferred: preferred}
		}
	}
	// 结构计算：D-412 F2 全叉积——每 (canonical, mt_venue, cex_venue) 三元组一行
	//（两种结构取 net 大者），任意授权三元组恒可命中（D-410 保底段三重键回归锚 ②）。
	for key, cfs := range fundByKey {
		for _, cf := range cfs {
			canonical := key
			preferred := key == cf.canonical
			mtVenues := map[string]bool{}
			for v := range longIdx[key] {
				mtVenues[v] = true
			}
			for v := range shortIdx[key] {
				mtVenues[v] = true
			}
			if len(mtVenues) == 0 {
				// D-382 S2：全禁标记行——三条件全满足才产（v2 钉死）：①canonical∈fundByCanon
				// （无 funding 本就不产行=正确静默）②有 swap fact 进入过滤评估 ③双向归并零存活。
				// 行语义=D-345 suspect 先例（脏行可见、net=0 沉底、告警天然不触）+D-350 (spot)
				// 哨兵 venue 先例；venue 日后解禁 → facts 更新 → 行自动复活为真候选。
				if filteredCanonicals[canonical] {
					emit(UniverseRow{
						Canonical: canonical, MtVenue: "(all-disabled)", CexVenue: cf.venue,
						NetAnnual: 0, FundingAnn: cf.val, FundingInst: cf.inst, SwapFree: false,
						Note: "mt_trade_disabled",
					}, "(disabled)|"+cf.venue+"|"+cf.sym, preferred)
				}
				continue
			}
			for venue := range mtVenues {
				if ml, ok := longIdx[key][venue]; ok {
					// 多 CFD+空 perp：net=funding_ann+swap_long（F1 年化后；swap_long≤0 成本负值
					// 直加——D1 方向性错误一并闭环，锚① INJ 算术=SSOT 写死 11−236.8=−225.8）
					row := UniverseRow{
						Canonical: canonical, MtVenue: ml.venue, CexVenue: cf.venue,
						NetAnnual: cf.val + ml.val, FundingAnn: cf.val, FundingInst: cf.inst,
						SwapLongPct: &ml.val,
						SwapFree:    abs(ml.val) < swapFreeMaxPct, Structure: "long_cfd_short_perp",
					}
					if note := suspectNote(ml.val, cf.val); note != "" { // D-345：脏源行 net=0 剔除排名
						row.NetAnnual, row.SwapFree, row.Note = 0, false, note
					}
					if ml.stale || cf.stale { // F4：龄期超闸=读口不诚实，剔除排名（D-345 同族形态）
						row.NetAnnual, row.SwapFree, row.Note = 0, false, "stale"
					}
					emit(row, ml.venue+"|"+cf.venue+"|"+cf.sym+"|"+row.Structure, preferred)
				}
				if ms, ok := shortIdx[key][venue]; ok {
					// 空 CFD+多 perp：net=swap_short_recv−funding_ann（funding<0 多头收）
					row := UniverseRow{
						Canonical: canonical, MtVenue: ms.venue, CexVenue: cf.venue,
						NetAnnual: ms.val - cf.val, FundingAnn: cf.val, FundingInst: cf.inst,
						SwapShortPct: &ms.val,
						SwapFree:     abs(ms.val) < swapFreeMaxPct, Structure: "short_cfd_long_perp",
					}
					if note := suspectNote(ms.val, cf.val); note != "" { // D-345：脏源行 net=0 剔除排名
						row.NetAnnual, row.SwapFree, row.Note = 0, false, note
					}
					if ms.stale || cf.stale { // F4
						row.NetAnnual, row.SwapFree, row.Note = 0, false, "stale"
					}
					emit(row, ms.venue+"|"+cf.venue+"|"+cf.sym+"|"+row.Structure, preferred)
				}
			}
		}
	}
	for _, c := range emitted {
		res.Rows = append(res.Rows, c.row)
	}
}
