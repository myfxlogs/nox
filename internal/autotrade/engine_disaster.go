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
	"time"

	"arb/internal/bus"
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

// disasterCheckXAsset D-378b S1：灾难带判定（替换 basisStopCheckXAsset；SWAPARB/CARRY 的
// basisStopCheck 不动——价差收敛策略反向持续移动=论点失败，止损语义成立；XASSET 论点=持有
// 收 carry，基差波动是噪声非论点失败）。
// 触发全条件（且）：①deviation=|currentBasis−anchor| > disasterBand ②双腿报价新鲜
// （任一腿 stale → 本轮不判，stale=true 交观测层记 fail_closed:stale）③连续
// XAssetDisasterDebounce 次满足 ①② 才平（单次尖刺不杀；计数器 disasterBreach 同
// fundingBreach 模式，清零/平仓删条目防泄漏；stale/未触带/无报价=连续性断→清零）。
// 返回 (平仓, reason, stale, staleAge)。reason 格式 "disaster: dev X > band Y"——
// 串本身不得含 basis/carry/funding/stop 子串（ExitKindFromReason contains 族截胡防护）。
func (e *Engine) disasterCheckXAsset(ctx context.Context, p *pgstore.Position) (shouldClose bool, reason string, stale bool, staleAge time.Duration) {
	e.disasterMu.Lock()
	defer e.disasterMu.Unlock()
	if e.disasterBreach == nil { // 惰性初始化（测试字面量 Engine 无 New 装配路径）
		e.disasterBreach = make(map[int64]int)
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
	// D-111：快行情挂起闸——任一腿窗口位移超阈 → 本轮不判+清计数（同 stale 连续性断）。
	// 业主领域裁定：Exness 类 CFD broker 大行情冻结/滞后报价 30min~1h 是常态作弊行为非结构
	// 断裂；此窗内跨所 basis 算不准（#509 误杀=MT 滞后价撕开 dev 42>40，2s 后 dev≈3.5）。
	// 新鲜度闸只能挡「时间戳旧」，挡不住「时间戳新但价格滞后」——位移闸正是为此。
	if e.fastMarketSuspend(ctx, p.ID, buyQ, sellQ) {
		delete(e.disasterBreach, p.ID)
		return false, "", false, 0
	}
	dev := math.Abs(basisBps - *p.ExpectedEntrySpreadBps)
	if dev <= band {
		delete(e.disasterBreach, p.ID) // 回常态=连续性断
		return false, "", false, 0
	}
	e.disasterBreach[p.ID]++
	debounce := e.cfg.XAssetDisasterDebounce
	if debounce < 1 {
		debounce = 1 // 防呆：≤1 → 单次即杀（向后兼容形态）
	}
	if e.disasterBreach[p.ID] >= debounce {
		delete(e.disasterBreach, p.ID) // 平仓 → 删除条目（防泄漏，同 fundingBreach）
		return true, fmt.Sprintf("disaster: dev %.1f > band %.1f", dev, band), false, 0
	}
	return false, "", false, 0
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
