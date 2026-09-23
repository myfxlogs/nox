// paper_all_in.go D-410：纸面全账口径共享核——venue 往返费率表 / 方向常数 / 收敛公式 /
// cfd pairKey。恒等式（R0-B1 钉死）：net_all_in = accrued_usd − est_round_trip_fee_usd
// + convergence_usd（费用列存正幅度）；capture_bps = dir×(basis_last−basis_entry)
// （R0-B2 钉死：带符号差×结构方向常数，弃绝对值式——翻号过冲域天然正确）。
package scan

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// paperCfdMonitorMaxAge cfd 轨 monitor basis 陈旧闸：行龄超限→NULL 诚实缺（与
// XAssetMonitorRowNear 的 maxGap 同值 15min，心跳报价糊弄不了 freshness 的隔壁同族）。
const paperCfdMonitorMaxAge = 15 * time.Minute

// paperCexVenueSet venue→往返费率常量表的覆盖集（=universe-radar CEX 源集）。
// 往返费率=2×cexFeeBps（开+平两笔，与 cfg.XAssetCexFeeBps 同源同值——migration 0128
// 回填 CASE 常量与本表同值，耦合钉死；env 改值须同步两处，测试断言一致）。
var paperCexVenueSet = map[string]bool{"binance": true, "okx": true, "gate": true, "mexc": true}

// paperRoundTripFeeBps venue→往返费率（bps）：覆盖集内返回 2×cexFeeBps；覆盖集外
// （含映射不出 cex 腿的组合串）→ ok=false（拒填行保持 NULL=可算不可毕业）。
func paperRoundTripFeeBps(venue string, cexFeeBps float64) (float64, bool) {
	v := venue
	if i := strings.LastIndex(v, "×"); i >= 0 {
		v = v[i+len("×"):] // cfd 行 venue=mt×cex 组合串，费用只在 cex 腿侧
	}
	if !paperCexVenueSet[v] {
		return 0, false
	}
	return 2 * cexFeeBps, true
}

// paperEstFeeUSD 开仓记账往返费用：qty×rate/1e4（venue 未覆盖 → nil 拒填）。
func paperEstFeeUSD(venue string, qtyUSD, cexFeeBps float64) *float64 {
	rate, ok := paperRoundTripFeeBps(venue, cexFeeBps)
	if !ok {
		return nil
	}
	fee := qtyUSD * rate / 1e4
	return &fee
}

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
func insertCfdPaper(ctx context.Context, st PaperStore, r UniverseRow, mtVenue, venueCombo string, now time.Time, cexFeeBps float64) error {
	side := "short_perp"
	if r.Structure == "short_cfd_long_perp" {
		side = "long_perp"
	}
	mv := mtVenue
	p := &pgstore.PaperSpotCarry{
		Venue: venueCombo, Symbol: r.Canonical, Side: side,
		EntryFundingAnn: r.FundingAnn, QtyUSD: PaperQtyUSD,
		Structure: r.Structure, MtVenue: &mv,
		EstRoundTripFeeUSD: paperEstFeeUSD(venueCombo, PaperQtyUSD, cexFeeBps),
	}
	// D-423：monitor 三元组——universe venue 裸名映射 venue 键（cex→+:perp，mt→+:mt5）。
	cexVenue, mtVenueKey := monitorVenueKeys(r.CexVenue, mtVenue)
	if eb, ok, err := st.XAssetMonitorRowNear(ctx, paperCfdPairKey(r.Canonical), cexVenue, mtVenueKey, now, paperCfdMonitorMaxAge); err != nil {
		return fmt.Errorf("monitor entry basis: %w", err)
	} else if ok {
		p.EntryBasisBps, p.LastBasisBps = &eb, &eb
	}
	return st.InsertPaperSpotCarry(ctx, p)
}

// paperExitConvergence 退出冻结收敛捕获：本轮 last 缺→回退行内 last，双缺→NULL 诚实缺。
func paperExitConvergence(p pgstore.PaperSpotCarry, lastBasis *float64) *float64 {
	lb := lastBasis
	if lb == nil {
		lb = p.LastBasisBps
	}
	return paperConvergenceUSD(paperBasisDir(p.Side, p.Structure), p.EntryBasisBps, lb, p.QtyUSD)
}

// authSlot D-410 S4 保底槽（D-419 S3 加 cexVenue 供 monitor 簿三元组哨兵）。
type authSlot struct{ key, venueCombo, symbol, mtVenue, cexVenue string }

// warnMonitorBookAbsent D-419 S3：授权组合 monitor 簿覆盖哨兵——簿三元组从未有过行
// → basis 恒 NULL 不可毕业（与宇宙行缺席同型静默，收口为可归因 warn）。
// 不阻断开仓（basis NULL 诚实缺语义保留）；查询失败 fail-open 跳过不误报；
// 聚合节流每轮至多一条。
func warnMonitorBookAbsent(ctx context.Context, st PaperStore, slots []authSlot) {
	var noMonitor []string
	for _, sl := range slots {
		pair := paperCfdPairKey(sl.symbol)
		cexV, mtV := monitorVenueKeys(sl.cexVenue, sl.mtVenue)
		has, err := st.HasXAssetMonitorRow(ctx, pair, cexV, mtV)
		if err != nil {
			log.Printf("[cfd paper] monitor 簿查询 %s|%s|%s: %v（fail-open 跳过）", pair, cexV, mtV, err)
			continue
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
	if cexVenue != "" && !strings.Contains(cexVenue, ":") {
		cexVenue += ":perp"
	}
	if mtVenue != "" && !strings.Contains(mtVenue, ":") {
		mtVenue += ":mt5"
	}
	return cexVenue, mtVenue
}

// cfdComboLegs D-410 钉死③venueCombo 映射：venue_buy/venue_sell 中带 ":mt4"/":mt5"
// 后缀者=mtVenue（去后缀得 raw id），另一腿去 ":perp"/":spot" 等尾缀=cexVenue；
// mtCanonical=MT 腿 canonical（symbol 键经 NormalizeJoinKey 规范化）。双腿皆无 MT
// 后缀（双 CEX）→ ok=false 跳过保底。
func cfdComboLegs(c pgstore.XAssetAllowRow) (mtVenue, cexVenue, mtCanon string, ok bool) {
	isMt := func(v string) bool { return strings.HasSuffix(v, ":mt4") || strings.HasSuffix(v, ":mt5") }
	strip := func(v string) string {
		if i := strings.LastIndex(v, ":"); i >= 0 {
			return v[:i]
		}
		return v
	}
	switch {
	case isMt(c.VenueBuy) && !isMt(c.VenueSell):
		return strip(c.VenueBuy), strip(c.VenueSell), c.CanonicalBuy, true
	case isMt(c.VenueSell) && !isMt(c.VenueBuy):
		return strip(c.VenueSell), strip(c.VenueBuy), c.CanonicalSell, true
	}
	return "", "", "", false
}
