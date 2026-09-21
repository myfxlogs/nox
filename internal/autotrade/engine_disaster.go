// engine_disaster.go —— D-378b S1/S2：XASSET 灾难带判定（替换 basisStopCheckXAsset 的位置与角色）。
// 判决依据（SSOT §1.4）：窄阈基差止损对 delta 中性 carry 仓结构性错配（带宽≈1σ 必被噪声扫、
// 四连杀全误杀实证、hummingbot 同构无此机制）——撤窄阈，只接结构性断裂级偏离，噪声带内浮亏
// 交给均值回复。判定「偏离」而非「亏损方向」（双向：基差暴涨同意味结构断裂）。
package autotrade

import (
	"context"
	"fmt"
	"math"
	"time"

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
	basisBps, quoteAge, ok := e.currentBasisBpsAged(p)
	if !ok || p.ExpectedEntrySpreadBps == nil {
		delete(e.disasterBreach, p.ID) // 连续性断（无法评估）
		return false, "", false, 0
	}
	band := e.disasterBand(p.EntryNoiseBps)
	if e.cfg.XAssetQuoteMaxAge > 0 && quoteAge > e.cfg.XAssetQuoteMaxAge {
		delete(e.disasterBreach, p.ID) // stale 不计数（连续性断；周末闭市=无法评估非灾难）
		return false, "", true, quoteAge
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
