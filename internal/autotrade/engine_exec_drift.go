// engine_exec_drift.go —— D-378a S2：XASSET 执行质量闸（exec_drift）+ harvestCheck 整组移入
// （engine_position.go 300 行门禁；harvest 为纯搬移零行为变更，XASSET 退出链出口同文件族）。
package autotrade

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// execDriftCheck D-378a S2：执行质量闸——XASSET 退出顺序 harvest 之后、basisStop 之前插入。
// fill 口径随时可重算（EntrySpreadBpsFromLegs 现成），漂移=fill−quote 锚（S1 起锚为纯报价）；
// |drift|>thresh → 双向都杀（正漂移=少收、负漂移=多付，都是执行质量破产）。
// thresh=max(cfg.XAssetExecDriftBps, entry_noise_bps)（noise 自适应抬阈：6.9 类噪声仓更保守；
// 实证 494 漂移≈0、495≈5~7、493≈14.3——阈 8 杀 493 型放 494/495 型）。
// 兼容性：存量仓旧锚本就是 fill 值 → drift≈0 → 永不误杀（天然向后兼容，勿「修复」）；
// 无锚（NULL）/腿未 FILLED（ok=false）→ 不判。
func (e *Engine) execDriftCheck(ctx context.Context, p *pgstore.Position) (bool, string) {
	if p.ExpectedEntrySpreadBps == nil {
		return false, ""
	}
	fillBps, ok, err := e.store.EntrySpreadBpsFromLegs(ctx, p.ID)
	if err != nil || !ok {
		return false, ""
	}
	drift := fillBps - *p.ExpectedEntrySpreadBps
	thresh := e.cfg.XAssetExecDriftBps
	if p.EntryNoiseBps != nil && *p.EntryNoiseBps > thresh {
		thresh = *p.EntryNoiseBps
	}
	if math.Abs(drift) > thresh {
		return true, fmt.Sprintf("exec_drift fill %.2fbps vs quote anchor %.2fbps", fillBps, *p.ExpectedEntrySpreadBps)
	}
	return false, ""
}

// harvestCheck D-341 S1（自 engine_position.go 纯搬移）+ D-404 S1b 真腿化：XASSET 收敛
// 止盈——仓内冻结锚三要素（entry_harvest_floor_bps/expected_entry_spread_bps/opened_at）
// 齐 → 走真腿尺度：开仓时点 monitor 行回放实测 venue 偏移，capture=entry_spread−floor
// （尺度不变量），收割线=|anchor|−capture，sign(anchor)·basis ≤ line 触发（过冲翻正同收）。
// 三要素任一缺 / 开仓时点 monitor 行缺或超窗 → 原 monitor 代理路径整体回退（旧仓兼容，
// 不用错尺度）；真腿路径确认后报价缺/超龄 → 本轮不判（fail-closed，不回退错尺度）。
// monitor 缺行/floor NULL → 不触发（诚实空态）。pairKey=D-335 同款（CanonicalSell-TrimSuffix）。
func (e *Engine) harvestCheck(ctx context.Context, p *pgstore.Position) (bool, string) {
	pairKey := canonicalFromOppID(p.OpportunityID)
	opp, err := e.store.GetOpportunity(ctx, p.OpportunityID)
	if err != nil || opp == nil {
		return false, ""
	}
	if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		pairKey = *opp.CanonicalSell + "-" + strings.TrimSuffix(opp.Canonical, "USD")
	}
	if p.EntryHarvestFloorBps != nil && p.ExpectedEntrySpreadBps != nil && *p.ExpectedEntrySpreadBps != 0 && p.OpenedAt != nil {
		if basis, line, capture, found, usable := e.xassetTrueLegLine(ctx, p, pairKey); found {
			if !usable {
				return false, "" // 报价缺/超龄 → 本轮不判（fail-closed，不回退错尺度）
			}
			anchor := *p.ExpectedEntrySpreadBps
			sign := 1.0
			if anchor < 0 {
				sign = -1.0
			}
			if sign*basis <= line {
				return true, fmt.Sprintf("harvest: basis %.2f sign-line %.2f (anchor %.2f capture %.2f)",
					basis, sign*line, anchor, capture)
			}
			return false, ""
		}
		// found=false（monitor 行缺/超窗）→ 落代理路径（旧仓兼容回退）
	}
	entryBps, _, floor, hasFloor, ok, err := e.store.LatestXAssetEntryGate(ctx, pairKey, xassetEntryGateMaxAge)
	if err != nil || !ok || !hasFloor {
		return false, ""
	}
	if entryBps <= floor {
		return true, fmt.Sprintf("harvest: spread %.2f ≤ floor %.2f (+%.2f bps captured)", entryBps, floor, floor-entryBps)
	}
	return false, ""
}

// xassetTrueLegLine D-404 真腿尺度收割线（D-406 S2 提取共享——rotateCheck 剩余收敛折算同源）。
// found=开仓时点 monitor 行在场（ts<=opened_at 最新行±15min）；usable=found 且报价新鲜可判。
// line=|anchor|−capture；capture=开仓时点 entry_spread−floor（尺度不变量平移）。
// 前置：调用方已确认三要素齐（floor/anchor≠0/opened_at 非 nil）。
func (e *Engine) xassetTrueLegLine(ctx context.Context, p *pgstore.Position, pairKey string) (basis, line, capture float64, found, usable bool) {
	entrySpread, ok, err := e.store.XAssetMonitorRowNear(ctx, pairKey, *p.OpenedAt, 15*time.Minute)
	if err != nil || !ok {
		return 0, 0, 0, false, false
	}
	basis, quoteAge, okB := e.currentBasisBpsAged(p)
	if !okB || (e.cfg.XAssetQuoteMaxAge > 0 && quoteAge > e.cfg.XAssetQuoteMaxAge) {
		return 0, 0, 0, true, false
	}
	capture = entrySpread - *p.EntryHarvestFloorBps
	return basis, math.Abs(*p.ExpectedEntrySpreadBps) - capture, capture, true, true
}
