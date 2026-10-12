// engine_disaster.go —— D-378b S1/S2：XASSET 灾难带判定（替换 basisStopCheckXAsset 的位置与角色）。
// 判决依据（SSOT §1.4）：窄阈基差止损对 delta 中性 carry 仓结构性错配（带宽≈1σ 必被噪声扫、
// 四连杀全误杀实证、hummingbot 同构无此机制）——撤窄阈，只接结构性断裂级偏离，噪声带内浮亏
// 交给均值回复。判定「偏离」而非「亏损方向」（双向：基差暴涨同意味结构断裂）。
package autotrade

import (
	"context"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"arb/internal/data/bus"
	"arb/internal/store/pgstore"
)

// disasterBand D-378b：判距=max(绝对下限, noise×倍数)。防呆：BPS≤0→用 40；MULT≤0→不乘
// （纯绝对阈）；entry_noise NULL → 用 cfg 值（fail-safe 不放宽）。参数依据：495 实测噪声
// 极值偏离 15.2bps≈2.2×noise——灾难带取 ≥4×noise 且绝对下限 40bps，正常噪声极值 2.6 倍外。
func (e *Engine) disasterBand(noise *float64) float64 {
	base := e.cfg.XAssetDisasterBps
	if base <= 0 {
		base = 40.0
	}
	if e.cfg.XAssetDisasterNoiseMult > 0 && noise != nil {
		return math.Max(base, e.cfg.XAssetDisasterNoiseMult**noise)
	}
	return base
}

// disasterCheckXAsset D-378b S1 + D-113 语义重置：XASSET 灾难带判定（SWAPARB/CARRY 的
// basisStopCheck 不动——价差收敛策略反向持续移动=论点失败，止损语义成立；XASSET 论点=持有
// 收 carry，基差波动是噪声非论点失败）。
// 分层：①MT/CFD 腿偏离 |basis−anchor|>band → 只告警不杀（业主领域裁定：broker
// 大行情冻结/滞后报价 30min~1h 是作弊常态非断裂，凡引用 MT 价的 kill 判定都会被假价穿透，
// #509 误杀实证）；②kill 唯一路径=CEX 腿 vs crypto 共识（同品种跨所+金代理源中位）的
// 锚后偏移>band；③快行情挂起闸（D-111）前置——窗内不判清零；④连续 debounce 轮才平
// （单次尖刺不杀；stale/共识不足/未触带/无报价=连续性断→清零）。
// 返回 (平仓, reason, stale, staleAge)。reason 格式 "disaster: cex-leg dev X > band Y"——
// 串本身不得含 basis/carry/funding/stop 子串（ExitKindFromReason contains 族截胡防护）。
func (e *Engine) disasterCheckXAsset(ctx context.Context, p *pgstore.Position) (shouldClose bool, reason string, stale bool, staleAge time.Duration) {
	e.disasterMu.Lock()
	defer e.disasterMu.Unlock()
	// 惰性初始化三张表各自判 nil（测试字面量 Engine 无 New 装配路径；New() 只预建
	// disasterBreach——三表并初始化曾让生产装配形态撞上 nil map panic：posLoop 死、
	// 退出评估全瘫 ~25min 才被发现。各表独立判 nil 防同类部分装配形态再穿透）。
	if e.disasterBreach == nil {
		e.disasterBreach = make(map[int64]int)
	}
	if e.cexBasisAnchor == nil {
		e.cexBasisAnchor = make(map[int64]float64)
	}
	if e.mtDivergeFlag == nil {
		e.mtDivergeFlag = make(map[int64]bool)
	}
	basisBps, quoteAge, buyQ, sellQ, ok := e.currentBasisBpsAgedQuotes(p)
	if !ok || p.ExpectedEntrySpreadBps == nil {
		delete(e.disasterBreach, p.ID) // 连续性断（无法评估）
		return false, "", false, 0
	}
	band := e.disasterBand(p.EntryNoiseBps)
	if e.cfg.XAssetQuoteMaxAge > 0 && quoteAge > e.cfg.XAssetQuoteMaxAge {
		delete(e.disasterBreach, p.ID) // stale 不计数（连续性断；周末闭市=无法评估非灾难）
		return false, "", true, quoteAge
	}
	// 快行情挂起闸——任一腿窗口位移超阈 → 本轮不判+清计数（同 stale 连续性断）。
	// 业主领域裁定：Exness 类 CFD broker 大行情冻结/滞后报价 30min~1h 是常态作弊行为非结构
	// 断裂；此窗内跨所 basis 算不准（#509 误杀=MT 滞后价撕开 dev 42>40，2s 后 dev≈3.5）。
	// 新鲜度闸只能挡「时间戳旧」，挡不住「时间戳新但价格滞后」——位移闸正是为此。
	if e.fastMarketSuspend(ctx, p.ID, buyQ, sellQ) {
		delete(e.disasterBreach, p.ID)
		delete(e.mtDivergeFlag, p.ID)
		return false, "", false, 0
	}
	// MT 侧偏离降级为「只告警不杀」——业主领域裁定：CFD 腿价在大行情/作弊窗
	// 结构性不可信（Exness 冻结/滞后 30min~1h 常态），凡引用它的 kill 判定都会被假价穿透
	// （#509 误杀实证）。kill 唯一路径=下方 CEX 腿 vs crypto 共识结构断裂。
	dev := math.Abs(basisBps - *p.ExpectedEntrySpreadBps)
	if dev > band {
		if !e.mtDivergeFlag[p.ID] {
			e.mtDivergeFlag[p.ID] = true
			msg := fmt.Sprintf("持仓 #%d MT 腿基差偏离 %.1fbps>带 %.1f——仅告警不杀（CFD 价不进 kill 判定，D-113）；若是 broker 滞后/作弊会自愈，持续偏离请人工裁定", p.ID, dev, band)
			if err := e.store.InsertPositionAlert(ctx, p.ID, "warn", msg); err != nil {
				log.Printf("[autotrade] pos %d mt-diverge alert write: %v", p.ID, err)
			}
		}
		log.Printf("[autotrade] pos=%d mt-leg dev=%.1f>band=%.1f (alert-only, D-113)", p.ID, dev, band)
	} else {
		delete(e.mtDivergeFlag, p.ID) // 回常态=episode 复位
	}
	// CEX 腿结构断裂=唯一 kill 判据：腿 mid − crypto 金代理共识中位 的锚后偏移 >band，
	// 连续 debounce 轮才平（真实脱锚才杀；两腿 fresh+非快行情仍是前置）。
	cexDev, nSrc, okCex := e.cexBreakDevBps(p.ID, buyQ, sellQ)
	if !okCex || cexDev <= band {
		delete(e.disasterBreach, p.ID) // 共识不足/未触带=连续性断
		return false, "", false, 0
	}
	e.disasterBreach[p.ID]++
	debounce := e.cfg.XAssetDisasterDebounce
	if debounce < 1 {
		debounce = 1 // 防呆：≤1 → 单次即杀（向后兼容形态）
	}
	if e.disasterBreach[p.ID] >= debounce {
		delete(e.disasterBreach, p.ID) // 平仓 → 删除条目（防泄漏，同 fundingBreach）
		delete(e.cexBasisAnchor, p.ID)
		// reason 串不得含 basis/carry/funding/stop 子串（ExitKindFromReason 截胡防护）
		return true, fmt.Sprintf("disaster: cex-leg dev %.1f > band %.1f (consensus n=%d)", cexDev, band, nSrc), false, 0
	}
	return false, "", false, 0
}

// xassetGoldConsensus D-113：金族 XASSET 的 CEX 腿附加代理共识源——XAUT(Tether Gold)/
// PAXG(PAX Gold) 真金价代币现货。与同品种跨 venue 源并集取中位（金腿 XAUUSDT 只 okx
// 有同名键，代理源补足共识面）。实测 2026-10-07：源间常驻价差 ~10-20bps（代币发行溢价）
// ——故判「锚后偏移」非绝对差。
var xassetGoldConsensus = []struct{ venueKey, symbol string }{
	{"binance:spot", "XAUTUSDT"},
	{"binance:spot", "PAXGUSDT"},
	{"gate:spot", "XAUTUSDT"},
	{"gate:spot", "PAXGUSDT"},
	{"okx:spot", "XAUTUSDT"},
	{"okx:spot", "PAXGUSDT"},
}

// cexConsensusMids D-113：CEX 腿共识源 mid 集——①同品种跨 venue 键（binance/gate/okx
// ×spot/perp，除腿自身键）：同 instrument 跨所价=最直接对照；②金族再并代理源表。
// 只收新鲜源（XAssetQuoteMaxAge 内）。
func (e *Engine) cexConsensusMids(cexQ bus.Quote) []float64 {
	sym := bus.CexSymbolKey(cexQ.Symbol)
	ownKey := cexQ.VenueKey()
	var srcs []struct{ venueKey, symbol string }
	for _, v := range []string{"binance", "gate", "okx"} {
		for _, vt := range []string{"spot", "perp"} {
			if vk := v + ":" + vt; vk != ownKey {
				srcs = append(srcs, struct{ venueKey, symbol string }{vk, sym})
			}
		}
	}
	base := strings.TrimSuffix(sym, "USDT")
	if base == "XAU" || base == "XAUT" || base == "PAXG" {
		srcs = append(srcs, xassetGoldConsensus...)
	}
	seen := make(map[string]bool, len(srcs)) // 同品种键与金代理源可撞键（binance:spot/PAXGUSDT）
	var mids []float64
	for _, s := range srcs {
		k := s.venueKey + "|" + s.symbol
		if seen[k] {
			continue
		}
		seen[k] = true
		q, okQ := e.quotes.Latest(s.symbol, s.venueKey)
		if !okQ || q.Mid() <= 0 {
			continue
		}
		if e.cfg.XAssetQuoteMaxAge > 0 && time.Since(q.Time) > e.cfg.XAssetQuoteMaxAge {
			continue
		}
		mids = append(mids, q.Mid())
	}
	return mids
}

// cexBreakDevBps D-113：CEX 腿 vs crypto 共识的结构断裂度。①双腿中取 venue_key 为
// :perp/:spot 的 CEX 腿 Quote；②共识=新鲜源 mid 中位数（≥2 源才可判，独苗歪价防呆）；
// ③首轮只锚定 (cexMid−median) 吸收常驻价差（perp-vs-代币溢价天然存在），dev=|偏移−锚|。
// 返回 (devBps, 共识源数, 可判定)。cexQ 缺失/源不足 → ok=false（连续性断）。
func (e *Engine) cexBreakDevBps(posID int64, quotes ...bus.Quote) (devBps float64, nSrc int, ok bool) {
	var cexQ *bus.Quote
	for i := range quotes {
		if bus.IsCexVenueKey(quotes[i].VenueKey()) && quotes[i].Mid() > 0 {
			cexQ = &quotes[i]
			break
		}
	}
	if cexQ == nil {
		return 0, 0, false
	}
	mids := e.cexConsensusMids(*cexQ)
	if len(mids) < 2 {
		return 0, len(mids), false
	}
	med := medianF64(mids)
	spread := cexQ.Mid() - med
	anchor, anchored := e.cexBasisAnchor[posID]
	if !anchored {
		e.cexBasisAnchor[posID] = spread // 首观测锚定（吸收常驻价差；重启再锚无害）
		return 0, len(mids), false
	}
	return math.Abs(spread-anchor) / med * 10000, len(mids), true
}

// medianF64 中位数（偶数取中两值均值）。
func medianF64(xs []float64) float64 {
	sort.Float64s(xs)
	n := len(xs)
	if n%2 == 1 {
		return xs[n/2]
	}
	return (xs[n/2-1] + xs[n/2]) / 2
}

// histPoint D-111：快行情位移采样点（venue 时序 mid）。quoteHist 挂 disasterMu 域（单写者=
// disasterCheckXAsset 调用路径，与 disasterBreach 同锁生命周期）。
type histPoint struct {
	ts  time.Time // Quote.Time（venue 时间戳，冻结即停采——同价重帧去重靠它）
	mid float64
}

// fastMarketSuspend D-111：快行情判定挂起。双腿各自 mid 位移采样进环形窗（key=venueKey|
// canonical 跨持仓共享），任一腿窗口位移 |最新−窗内最早|/最新 >XAssetFastMoveBps → suspend。
// suspend 语义=连续性断（清 breach、episode 首报 warn 一次、出窗复位可再报）。
// cfg 任一 ≤0 → 闸关（fail-open 回旧行为，逃生阀）。
func (e *Engine) fastMarketSuspend(ctx context.Context, posID int64, quotes ...bus.Quote) bool {
	if e.cfg.XAssetFastMoveBps <= 0 || e.cfg.XAssetFastMoveWindow <= 0 {
		return false
	}
	if e.quoteHist == nil {
		e.quoteHist = make(map[string][]histPoint)
		e.fastMoveFlag = make(map[int64]bool)
	}
	for _, q := range quotes {
		if q.Mid() <= 0 {
			continue
		}
		key := q.Venue + ":" + q.VenueType + "|" + q.Symbol
		h := e.quoteHist[key]
		if n := len(h); n == 0 || q.Time.After(h[n-1].ts) { // 同刻/回退帧不采（冻结期零增长）
			h = append(h, histPoint{ts: q.Time, mid: q.Mid()})
		}
		cut := q.Time.Add(-e.cfg.XAssetFastMoveWindow)
		for len(h) > 1 && h[0].ts.Before(cut) {
			h = h[1:]
		}
		e.quoteHist[key] = h
		if len(h) < 2 {
			continue
		}
		last := h[len(h)-1]
		disp := math.Abs(last.mid-h[0].mid) / last.mid * 10000
		if disp <= e.cfg.XAssetFastMoveBps {
			continue
		}
		if !e.fastMoveFlag[posID] {
			e.fastMoveFlag[posID] = true
			msg := fmt.Sprintf("品种 %s 快行情中（%ds 窗 %s 位移 %.1fbps>%.1f），灾难判定挂起——CFD 腿报价此窗不可信（broker 大行情冻结/滞后），回稳自动恢复",
				q.Symbol, int(e.cfg.XAssetFastMoveWindow.Seconds()), key, disp, e.cfg.XAssetFastMoveBps)
			if err := e.store.InsertPositionAlert(ctx, posID, "warn", msg); err != nil {
				log.Printf("[autotrade] pos %d fastmove alert write: %v", posID, err)
			}
		}
		log.Printf("[autotrade] pos=%d fast-move suspend: %s disp=%.1fbps>%.1f", posID, key, disp, e.cfg.XAssetFastMoveBps)
		return true
	}
	delete(e.fastMoveFlag, posID) // 双腿均在窗内 → episode 出，复位首报去重
	return false
}

// fmtDisasterBand D-378b：判距来源外化（观测行 detail 必须含 max(cfg,noise×mult) 格式）。
func fmtDisasterBand(cfgBps, mult float64, noise *float64) string {
	base := cfgBps
	if base <= 0 {
		base = 40.0
	}
	if mult > 0 && noise != nil {
		return fmt.Sprintf("max(%.1f,%.1f×noise %.1f)", base, mult, *noise)
	}
	return fmt.Sprintf("cfg %.1f", base)
}
