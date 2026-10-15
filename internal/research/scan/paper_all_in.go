// paper_all_in.go D-410：纸面全账口径共享核——venue 往返费率表 / 方向常数 / 收敛公式 /
// cfd pairKey。恒等式（R0-B1 钉死）：net_all_in = accrued_usd − est_round_trip_fee_usd
// + convergence_usd（费用列存正幅度）；capture_bps = dir×(basis_last−basis_entry)
// （R0-B2 钉死：带符号差×结构方向常数，弃绝对值式——翻号过冲域天然正确）。
package scan

import (
	"context"
	"fmt"
	"log"
	"nox/internal/data/keydomain"
	"sort"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// paperCfdMonitorMaxAge cfd 轨 monitor basis 陈旧闸：行龄超限→NULL 诚实缺（与
// XAssetMonitorRowNear 的 maxGap 同值 15min，心跳报价糊弄不了 freshness 的隔壁同族）。
const paperCfdMonitorMaxAge = 15 * time.Minute

// paperConvergenceUSD 收敛捕获=dir×(last−entry)×qty/1e4（basis 双 NULL→nil 诚实缺）。
func paperConvergenceUSD(dir float64, entry, last *float64, qtyUSD float64) *float64 {
	if entry == nil || last == nil {
		return nil
	}
	v := dir * (*last - *entry) * qtyUSD / 1e4
	return &v
}

// paperBasisDir 方向常数（结构×基差定义双锁定，记忆法：空腿=基差被减项→dir=−1）：
// spot 轨 basis=perp 对 index 溢价——short_perp（空腿）→−1（溢价缩=赚）、long_perp→+1；
// cfd 轨 basis=monitor s=cex−mt（市场面与持仓方向无关）——long_cfd_short_perp
// （空 perp 腿）→−1、short_cfd_long_perp→+1。其他结构 → 0（调用方拒算）。
func paperBasisDir(side, structure string) float64 {
	switch structure {
	case "long_cfd_short_perp":
		return -1
	case "short_cfd_long_perp":
		return 1
	}
	if side == "long_perp" {
		return 1
	}
	return -1
}

// paperCfdPairKey cfd 轨 monitor pair 键：cex侧canonical+"-"+TrimSuffix(mt侧canonical,"USD")
// ——与 autotrade.xassetPairKey 同构造规则（生产 #11：XAUUSDT:okx:perp×XAUUSD:mt5 →
// "XAUUSDT-XAU"）。纸面行 symbol=universe canonical（规范化无 USDT/USD 后缀），
// cex 侧按既有 cexCanonical 规则补 "USDT"（ResolveGraduationCanonicals 同规则）。
func paperCfdPairKey(symbol string) string {
	return symbol + "USDT-" + strings.TrimSuffix(symbol, "USD")
}

// netAllInUSD 全账净额（缺项按 0 计——展示/可算口径；毕业判据对 NULL 行另拒）。
func netAllInUSD(accrued float64, estFee, convergence *float64) float64 {
	net := accrued
	if estFee != nil {
		net -= *estFee
	}
	if convergence != nil {
		net += *convergence
	}
	return net
}

// insertCfdPaper cfd 纸面开仓（保底段与 top-N 段共用）：venue=mt×cex 组合串、
// est_round_trip_fee 开仓记账（venue 未覆盖→NULL 拒填）、entry/last_basis 接 monitor 面
// （XAssetMonitorRowNear ts<=entry_ts 谓词复用 D-404 R1 冻结锚同源；缺席→NULL 诚实缺）。
func insertCfdPaper(ctx context.Context, st PaperStore, r UniverseRow, mtVenue, cexVenue string,
	view *assetClassView, now time.Time) error {
	side := "short_perp"
	if r.Structure == "short_cfd_long_perp" {
		side = "long_perp"
	}
	mv := mtVenue
	p := &pgstore.PaperSpotCarry{
		VenueA: mtVenue, VenueB: cexVenue, Symbol: r.Canonical, Side: side,
		// D-060②：entry 列记入场闸判定值（7d 均值净 carry=NetAnnual）——原记瞬时值
		// FundingInst 周末开仓落 0 → 面板承诺列恒「—」读不出承诺（D-034 展示列口径让位承诺口径）。
		EntryFundingAnn: r.NetAnnual, QtyUSD: PaperQtyUSDAt(ctx, st),
		Structure: r.Structure, MtVenue: &mv,
		Note: "MT佣金未计（无常数实值）", // D-474a S2：分腿真费佣金项缺省注记
	}
	// monitor 三元组——universe venue 裸名映射 venue 键（cex→+:perp，mt→+:mt5）。
	var spread *float64
	cexVenue, mtVenueKey := monitorVenueKeys(r.CexVenue, mtVenue)
	// ②：pair 键候选（class 行经 asset_equiv 实解析），按序 first-hit。
	for _, pair := range view.cfdPairCandidates(r.Canonical, cexVenue, mtVenueKey) {
		if eb, ok, err := st.XAssetMonitorRowNear(ctx, pair, cexVenue, mtVenueKey, now, paperCfdMonitorMaxAge); err != nil {
			return fmt.Errorf("monitor entry basis: %w", err)
		} else if ok {
			p.EntryBasisBps, p.LastBasisBps = &eb, &eb
			spread = &eb // D-474a S2：MT 腿费用=monitor 簿实算点差（同一行值）
			break
		}
	}
	// cex 往返 2 笔 + MT 腿簿实算点差往返 + 佣金常数；簿缺→NULL 缺账。
	p.EstRoundTripFeeUSD = paperCfdFeeUSD(ctx, st, r.CexVenue, PaperQtyUSDAt(ctx, st), spread)
	// 毕业取证病灶修（10-15：多行 est_fee∈−2.5~−39）：费为负=模型自相矛盾
	// （monitor 簿 entry_basis 负值曾被当 spread 分量直接喂入）→ fail-closed
	// 拒开仓+warn，历史负行注记不改判据。
	if p.EstRoundTripFeeUSD != nil && *p.EstRoundTripFeeUSD < 0 {
		_ = st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
			"[paper] est_fee 负值拒开仓：%s %s fee=%.4f（簿/费率面异常如实记）",
			r.Canonical, side, *p.EstRoundTripFeeUSD))
		return nil
	}
	return st.InsertPaperSpotCarry(ctx, p)
}

// PaperExitConvergence D-474b S1：退出冻结收敛对外导出（universe-radar perp_perp 轨复用，
// 单判据源防公式漂移）。
func PaperExitConvergence(p pgstore.PaperSpotCarry, lastBasis *float64) *float64 {
	return paperExitConvergence(p, lastBasis)
}

// paperExitConvergence 退出冻结收敛捕获：本轮 last 缺→回退行内 last，双缺→NULL 诚实缺。
func paperExitConvergence(p pgstore.PaperSpotCarry, lastBasis *float64) *float64 {
	lb := lastBasis
	if lb == nil {
		lb = p.LastBasisBps
	}
	return paperConvergenceUSD(paperBasisDir(p.Side, p.Structure), p.EntryBasisBps, lb, p.QtyUSD)
}

// WarnDataLostExits D-538①：data_lost 批量退聚合 warn——退出>0 时一轮一轨至多一条
// （断链退仓不再静默；聚合同毕业链惯例不逐行刷）。keys=venue对|symbol 归因名。
// 参数面=最小 InsertAlert 接口（scan 各轨 PaperStore 与 radar *pgstore.Store 同吃）。
func WarnDataLostExits(ctx context.Context, st interface {
	InsertAlert(ctx context.Context, ruleID *int64, level, message string) error
}, track string, keys []string) {
	if len(keys) == 0 {
		return
	}
	sort.Strings(keys)
	if err := st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"[%s 纸面] %d 行 data_lost 断链退出（喂流断链/数据缺席可归因）: %s",
		track, len(keys), strings.Join(keys, ", "))); err != nil {
		log.Printf("[paper] data_lost warn(%s): %v", track, err)
	}
}

// paperExitNote D-056④：退出判定值留痕——统一「exit=<reason> net=%.2f
// funding7d=%.2f swap=%.2f streak=%d」幂等追加行 note（net=当轮净判定值年化%；
// funding7d=funding 腿 7d 滚动均值年化；swap=swap 腿年化；streak=触发计数器；
// 缺位轮各值取行内最后观测/0 诚实缺）。注记失败仅 log——退出已提交，审计增强
// 不反噬退出本身。
func paperExitNote(ctx context.Context, st PaperStore, id int64, reason string, net, funding7d, swap float64, streak int) {
	if err := st.AnnotatePaperNote(ctx, id, fmt.Sprintf(
		"exit=%s net=%.2f funding7d=%.2f swap=%.2f streak=%d", reason, net, funding7d, swap, streak)); err != nil {
		log.Printf("[paper] exit note %d: %v", id, err)
	}
}

// lastFundingAnnOf 行内最后观测 funding 年化（缺位轮判定值缺省源；NULL→0）。
func lastFundingAnnOf(p pgstore.PaperSpotCarry) float64 {
	if p.LastFundingAnn != nil {
		return *p.LastFundingAnn
	}
	return 0
}

// cfdSwapLegPct cfd 行 MT 腿侧 swap 年化（结构定腿：long_cfd_short_perp=MT 腿多
// →swap_long；short_cfd_long_perp→swap_short；NULL→0 诚实缺）。
func cfdSwapLegPct(r UniverseRow) float64 {
	v := r.SwapLongPct
	if r.Structure == "short_cfd_long_perp" {
		v = r.SwapShortPct
	}
	if v == nil {
		return 0
	}
	return *v
}

// authSlot D-410 S4 保底槽（D-419 S3 加 cexVenue 供 monitor 簿三元组哨兵；
// cls 退役——class 键授权计数随数量槽一起删）。
type authSlot struct{ key, venueCombo, symbol, mtVenue, cexVenue string }

// warnMonitorBookAbsent D-419 S3：授权组合 monitor 簿覆盖哨兵——簿三元组从未有过行
// → basis 恒 NULL 不可毕业（与宇宙行缺席同型静默，收口为可归因 warn）。
// 不阻断开仓（basis NULL 诚实缺语义保留）；查询失败 fail-open 跳过不误报；
// 聚合节流每轮至多一条。
func warnMonitorBookAbsent(ctx context.Context, st PaperStore, slots []authSlot, view *assetClassView) {
	var noMonitor []string
	for _, sl := range slots {
		cexV, mtV := monitorVenueKeys(sl.cexVenue, sl.mtVenue)
		has, pair := false, ""
		for _, pair = range view.cfdPairCandidates(sl.symbol, cexV, mtV) { // D-043 ②：候选任中即覆盖
			hit, err := st.HasXAssetMonitorRow(ctx, pair, cexV, mtV)
			if err != nil {
				log.Printf("[cfd paper] monitor 簿查询 %s|%s|%s: %v（fail-open 跳过）", pair, cexV, mtV, err)
				has = true // 查询失败 fail-open 不误报
				break
			}
			if hit {
				has = true
				break
			}
		}
		if !has {
			noMonitor = append(noMonitor, sl.key+"（簿:"+pair+"|"+cexV+"|"+mtV+"）")
		}
	}
	if len(noMonitor) > 0 {
		if err := st.InsertAlert(ctx, nil, "warn", "[cfd 纸面保底] 授权组合 monitor 簿缺席（basis 将恒 NULL 不可毕业，请补 monitorPairs）: "+strings.Join(noMonitor, ", ")); err != nil {
			log.Printf("[cfd paper] monitor-book alert: %v", err)
		}
	}
}

// monitorVenueKeys D-423：universe 裸 venue 名（"okx"/"3001157"）→ monitor 行 venue 键
// （cex→+:perp 与仓腿/机会 venue 形态对齐；mt→+:mt5；已带冒号原样=诚实不猜）。
func monitorVenueKeys(cexV, mtV string) (cexVenue, mtVenue string) {
	cexVenue, mtVenue = cexV, mtV
	if cexVenue != "" && keydomain.Parse(cexVenue).Type == "" {
		cexVenue = keydomain.Join(cexVenue, "perp")
	}
	if mtVenue != "" && keydomain.Parse(mtVenue).Type == "" {
		mtVenue = keydomain.Join(mtVenue, "mt5")
	}
	return cexVenue, mtVenue
}

// cfdComboLegs D-410 钉死③venueCombo 映射：venue_buy/venue_sell 中带 ":mt4"/":mt5"
// 后缀者=mtVenue（去后缀得 raw id），另一腿去 ":perp"/":spot" 等尾缀=cexVenue；
// mtCanonical=MT 腿 canonical（symbol 键经 NormalizeJoinKey 规范化）。双腿皆无 MT
// 后缀（双 CEX）→ ok=false 跳过保底。
func cfdComboLegs(c pgstore.XAssetAllowRow) (mtVenue, cexVenue, mtCanon string, ok bool) {
	isMt := func(v string) bool { return keydomain.IsMTKey(v) }
	strip := keydomain.BareOf
	switch {
	case isMt(c.VenueBuy) && !isMt(c.VenueSell):
		return strip(c.VenueBuy), strip(c.VenueSell), c.CanonicalBuy, true
	case isMt(c.VenueSell) && !isMt(c.VenueBuy):
		return strip(c.VenueSell), strip(c.VenueBuy), c.CanonicalSell, true
	}
	return "", "", "", false
}

// cexSpotPerpComboLegs D-555 S1：cfdComboLegs 对偶件——双CEX spot×perp 同 canonical 构型解析。
// combo 行双腿为 venue_key（typed keys，LoadXAssetAllowCombos 投影 venue_key_*）：
// 恰一 :spot+恰一 :perp 且 canonical_buy==canonical_sell（FUNDING_HEDGE 同 canonical 构型）
// → (spotBare, perpBare, canonical, spotType, perpType)——a/b 序按腿型归一（行序无关）。
// 其余构型（双腿 MT / 双 perp / 跨 canonical）→ ok=false（mt×cex 归 cfdComboLegs，余不管）。
func cexSpotPerpComboLegs(c pgstore.XAssetAllowRow) (spotBare, perpBare, canonical, spotType, perpType string, ok bool) {
	if c.CanonicalBuy != c.CanonicalSell || c.CanonicalBuy == "" {
		return
	}
	buy := keydomain.Parse(c.VenueBuy)
	sell := keydomain.Parse(c.VenueSell)
	switch {
	case buy.Type == "spot" && sell.Type == "perp":
		return buy.Venue, sell.Venue, c.CanonicalBuy, buy.Type, sell.Type, true
	case sell.Type == "spot" && buy.Type == "perp":
		return sell.Venue, buy.Venue, c.CanonicalBuy, sell.Type, buy.Type, true
	}
	return
}
