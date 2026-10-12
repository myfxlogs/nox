// paper_cfd_book_gate.go —— D-144 ③④：monitor 簿覆盖闸（开仓拒不可覆盖组合记因）
// 与 active 仓簿缺席自盯告警。判据=簿结构性可覆盖性：CEX 侧在报价源集（binance/
// okx/gate 读库 fetcher 三所）且 fin_cex_universe 有 TRADING 合约行，MT 侧 ticks
// 保留窗（72h，ops_ticks_retention）内该账户采到过 exSym——测不了的仓不验证
// （entry_basis 恒 NULL 注定无账=精确闸非误杀）。查询错误 fail-open 不拦开仓
// （基础设施抖动≠结构性不可覆盖；读败仅 log）。
package scan

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

// cfdCexQuoteSources monitor 簿 CEX 报价源集（xasset-monitor fetcher 三所读库轨）。
var cfdCexQuoteSources = map[string]bool{"binance": true, "okx": true, "gate": true}

// bookMTTickLookback MT 品种结构性存在判据窗=ticks 保留窗 72h（周末休市 48-54h
// 仍在窗内不误杀；超窗=该账户从未采过此品种=结构性不可覆盖）。
const bookMTTickLookback = 72 * time.Hour

// CexNativeSym DB 键形 perp canonical（"XAGUSDT"）→venue 原生合约名
// （fin_cex_universe.symbol 键形：okx instId / gate contract / binance pair）。
// 未知 venue→""（不在报价源集，调用方另判）。
func CexNativeSym(venue, canon string) string {
	base := strings.TrimSuffix(canon, "USDT")
	switch venue {
	case "okx":
		return base + "-USDT-SWAP"
	case "gate":
		return base + "_USDT"
	case "binance":
		return canon
	}
	return ""
}

// bookUncoverableReason 簿结构性不可覆盖原因（空串=可覆盖放行）。入参 venue 皆
// 裸名（"15032313"/"gate"）；canonical=纸面行 symbol 口径（universe canonical）。
// uni=nil 表示合约面读失败——cex 合约腿不判（基础设施抖动≠结构性不可覆盖，fail-open 不误杀）。
func bookUncoverableReason(ctx context.Context, st PaperStore, uni map[string]pgstore.CexUniverseRow,
	view *assetClassView, canonical, mtVenue, cexVenue string, now time.Time) string {
	cexBare := keydomain.BareOf(cexVenue)
	mtBare := keydomain.BareOf(mtVenue)
	var reasons []string
	if !cfdCexQuoteSources[cexBare] {
		reasons = append(reasons, "cex 不在报价源:"+cexBare)
	} else if uni != nil {
		// 合约命中口径同 cexRawCanonIndex（Symbol/HedgeCanonical/facts 键三式归一）——
		// venue 原生合约名不以 canonical+"USDT" 猜（fixture raw 名 D374X/生产 gate BTC_USDT 同判）。
		covered := map[string]bool{}
		for _, ur := range uni {
			if ur.Status != "TRADING" {
				continue
			}
			for _, canon := range []string{ur.HedgeCanonical, ur.Symbol, NormalizeJoinKey(cexFactSymbol(ur.Symbol))} {
				if canon != "" {
					covered[ur.Venue+"|"+NormalizeJoinKey(canon)] = true
				}
			}
		}
		cexOK := false
		for _, sp := range view.cfdBookSpecs(canonical, cexBare, mtBare) {
			// 键侧双重 NJK 与 covered 归一同型（XAUUSDUSDT→XAUUSD→XAU；
			// 单重 NJK 得 XAUUSD 与 covered 键 gate|XAU 永不相交）。
			if covered[cexBare+"|"+NormalizeJoinKey(NormalizeJoinKey(cexFactSymbol(sp.CexCanon)))] {
				cexOK = true
				break
			}
		}
		if !cexOK {
			reasons = append(reasons, "cex 无 TRADING 合约行:"+cexBare)
		}
	}
	mtOK := false
	for _, sym := range cfdMtTickCandidates(ctx, st, view, canonical, mtBare) {
		ticks, err := st.TicksWindow(ctx, mtBare, sym, now.Add(-bookMTTickLookback))
		if err != nil {
			log.Printf("[cfd paper] 簿覆盖闸 ticks 查询 %s|%s: %v（fail-open 放行）", mtBare, sym, err)
			mtOK = true
			break
		}
		if len(ticks) > 0 {
			mtOK = true
			break
		}
	}
	if !mtOK {
		reasons = append(reasons, fmt.Sprintf("mt 无对应 tick 品种:%s（72h 保留窗零采样）", mtBare))
	}
	return strings.Join(reasons, ";")
}

// cfdMtTickCandidates MT 腿 tick 符号候选（ticks.symbol=broker_symbol_map.canonical
// 形 "XAUUSD"，与 universe canonical "XAU" 双形分裂——NJK 归一匹配该账户映射表；
// spec.ExSym=等价类 MT canonical 亦入候选；canonical 本身兜底）。去重定序返回。
func cfdMtTickCandidates(ctx context.Context, st PaperStore, view *assetClassView, canonical, mtBare string) []string {
	out := []string{canonical}
	seen := map[string]bool{canonical: true}
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	for _, sp := range view.cfdBookSpecs(canonical, "", mtBare) {
		add(sp.ExSym)
	}
	if maps, err := st.ListSymbolMappings(ctx, mtBare); err == nil {
		for _, m := range maps {
			if NormalizeJoinKey(m.Canonical) == NormalizeJoinKey(canonical) {
				add(m.Canonical)
			}
		}
	}
	return out
}

// warnActiveBookAbsent D-144 ④：active cfd 纸面仓三元组簿存在性自盯（D-033 系统自盯
// 口径——结构性不可覆盖/venue 断供致簿缺行的仓 basis 冻结 NULL 不可毕业，收口为可归因
// warn）。不阻断任何流程；查询失败 fail-open 不误报；聚合节流每轮至多一条。
func warnActiveBookAbsent(ctx context.Context, st PaperStore, active []pgstore.PaperSpotCarry, view *assetClassView) {
	var noBook []string
	for _, p := range active {
		if !cfdStructures[p.Structure] || p.VenueA == "" || p.VenueB == "" {
			continue
		}
		cexV, mtV := monitorVenueKeys(p.VenueB, p.VenueA)
		has, pair := false, ""
		for _, pair = range view.cfdPairCandidates(p.Symbol, cexV, mtV) {
			hit, err := st.HasXAssetMonitorRow(ctx, pair, cexV, mtV)
			if err != nil {
				log.Printf("[cfd paper] active 簿查询 %s|%s|%s: %v（fail-open 跳过）", pair, cexV, mtV, err)
				has = true
				break
			}
			if hit {
				has = true
				break
			}
		}
		if !has {
			noBook = append(noBook, p.VenueA+"×"+p.VenueB+"|"+p.Symbol+"（簿:"+pair+"|"+cexV+"|"+mtV+"）")
		}
	}
	if len(noBook) > 0 {
		if err := st.InsertAlert(ctx, nil, "warn", "[cfd 纸面] active 仓 monitor 簿缺席（entry basis 冻结 NULL 不可毕业，请查 venue 断供/覆盖闸）: "+strings.Join(noBook, ", ")); err != nil {
			log.Printf("[cfd paper] active-book alert: %v", err)
		}
	}
}
