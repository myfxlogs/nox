// engine_rotate.go —— D-406：持仓轮转比较器（capital rotation，XASSET 退出链第五闸）。
// 轮转=纯出场决策：四闸（carry_death/harvest/exec_drift/disaster）全过后才评；
// 触发 → close(reason="rotate_to:<oppID>") 走既有平仓路径，备腿开仓零新代码
// （detector 对白名单组合天然产 opp → engine 白名单放行自然接上）。
// fail-closed 贯穿：替代 EV 数据缺/资金前置缺 → 不轮转；detail 三值落 position_evals 观测。
package autotrade

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"arb/internal/combo"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// 轮转参数（SSOT §1 钉死值：H=30d 摊销窗 + 2×安全边际 + 组合对 24h 冷却）。
const (
	rotateHorizonDays   = 30.0
	rotateSafetyFactor  = 2.0
	rotatePairCooldown  = 24 * time.Hour
	rotateCountWindow   = 24 * time.Hour
	rotateXferFeeUSD    = 1.0 // USDT-TRC20 划转费常量表值（SSOT §0/N2；$4k 尺度≈2.3bps 与交易费同量级）
	rotateCexFeeBpsDflt = 5.0 // CEX taker 常量表回退（CommissionCEXTakerBps 缺省同值，VIP0 实测）
	rotateMtFeeBpsDflt  = 0.3 // MT 腿常量表回退（CommissionCFDBps 缺省同值）
)

// rotateOutcome 轮转评估结果（观测行 detail 三值载体；evaluated=false=未评/不适用不写行）。
type rotateOutcome struct {
	triggered bool
	altOppID  string
	detail    string // alt/cur/cost 三值 + 拦阻原因短串（≤200 字由 store 截断）
}

// rotateCheck 轮转第五闸：仅前四闸全过后调用。返回 (shouldRotate, reason)。
func (e *Engine) rotateCheck(ctx context.Context, p *pgstore.Position) (bool, *rotateOutcome) {
	out := &rotateOutcome{}
	fail := func(format string, args ...any) (bool, *rotateOutcome) {
		out.detail = fmt.Sprintf(format, args...)
		return false, out
	}
	// ①腿全 FILLED（无未决执行）——scanAndClose 只扫 OPEN（CLOSING/LEGGED 天然不入）。
	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return fail("legs query err %v", err)
	}
	openLegs := 0
	for _, l := range legs {
		if l.Phase != "OPEN" {
			continue
		}
		openLegs++
		if l.Status != "FILLED" {
			return fail("legs not all FILLED（未决执行，不轮转）")
		}
	}
	if openLegs == 0 {
		return fail("no open legs")
	}
	if p.NotionalUSD == nil || *p.NotionalUSD <= 0 {
		return fail("notional_usd 缺（fail-closed）")
	}
	notional := *p.NotionalUSD

	// ②curEV = curNetCarry%/yr + 剩余收敛折算（v1 必含，R0-N3：
	// (|basis|−line)bps → 30d 摊年化；收割线同源 xassetTrueLegLine）。
	curCarry, ok := e.recalcXAssetNetCarry(ctx, p)
	if !ok {
		return fail("cur carry 数据缺（fail-closed）")
	}
	pairKey := canonicalFromOppID(p.OpportunityID)
	if opp, oerr := e.store.GetOpportunity(ctx, p.OpportunityID); oerr == nil && opp != nil &&
		opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		pairKey = *opp.CanonicalSell + "-" + strings.TrimSuffix(opp.Canonical, "USD")
	}
	var curEV float64
	if p.EntryHarvestFloorBps != nil && p.ExpectedEntrySpreadBps != nil &&
		*p.ExpectedEntrySpreadBps != 0 && p.OpenedAt != nil {
		basis, line, _, found, usable := e.xassetTrueLegLine(ctx, p, pairKey)
		if !found || !usable {
			return fail("收割线/报价缺（剩余收敛不可算，fail-closed）")
		}
		remBps := math.Abs(basis) - line
		if remBps < 0 {
			remBps = 0 // 已收敛过线（理论不可达——harvest 应先触发）
		}
		// 剩余收敛 bps → %/yr：bps/1e4 比例 ×365/30 摊 ×100 → bps×365/3000。
		curEV = curCarry + remBps*365.0/(rotateHorizonDays*100.0)
	} else {
		return fail("收割锚三要素缺（旧仓 floor 未落，fail-closed 不轮转）")
	}

	// ③替代枚举：opportunities 新鲜行 × xasset_allow 白名单（授权键不带 EV——
	// 数值永远取自 opportunities 行，combo 只做过滤；R0 五轮钉死）。
	opps, err := e.store.ListOpportunities(ctx, "XASSET_CARRY", 200, 0)
	if err != nil {
		return fail("list opportunities err %v", err)
	}
	specs := e.xassetSpecs()
	curKey := combo.FromOppID(p.OpportunityID)
	var bestAltEV float64
	var bestOpp *pgstore.Opportunity
	var bestSpec *exec.XAssetComboAllowSpec
	freshTTL := e.cfg.FreshnessTTL
	if freshTTL <= 0 {
		freshTTL = 2 * time.Minute // 未配置回退 detector 缺省同值
	}
	for i := range opps {
		o := &opps[i]
		if time.Since(o.QuoteTime) > freshTTL {
			continue // 非新鲜行（防「平进空气」前置：替代须当前新鲜存在）
		}
		if combo.FromOppID(o.ID) == curKey || o.ID == p.OpportunityID {
			continue // 本仓组合排除
		}
		sellCan := o.Canonical
		if o.CanonicalSell != nil && *o.CanonicalSell != "" {
			sellCan = *o.CanonicalSell
		}
		spec := exec.MatchXAssetComboAllow(specs, o.Canonical, sellCan, o.VenueBuy, o.VenueSell)
		if spec == nil {
			continue // 白名单外（发现≠授权）
		}
		altEV, ok := e.xassetComboNetCarry(ctx, o.Canonical, sellCan, o.VenueBuy, o.VenueSell)
		if !ok {
			continue // 替代腿 carry 数据缺（fail-closed 跳过该候选）
		}
		if bestOpp == nil || altEV > bestAltEV {
			bestAltEV, bestOpp, bestSpec = altEV, o, spec
		}
	}
	if bestOpp == nil {
		return fail("无新鲜已批准替代（cur=%.2f）", curEV)
	}

	// ④switchCost = 双腿往返交易费 + 划转费（常量表；resolver 缺走常量禁零值）。
	cost := e.rotateSwitchCostUSD(bestSpec.VenueBuy, bestSpec.VenueSell, notional)

	// ⑤日限 + 组合对 24h 冷却（position_evals rotate 行持久计数，免新表）。
	since := time.Now().Add(-rotateCountWindow)
	if maxN := e.cfg.RotateMaxPerDay; maxN > 0 {
		if cnt, cerr := e.store.RotateEvalCount(ctx, since); cerr != nil {
			return fail("rotate count query err %v（fail-closed）", cerr)
		} else if cnt >= maxN {
			return fail("日轮转限满 %d/%d（cur=%.2f alt=%.2f cost=%.2f）", cnt, maxN, curEV, bestAltEV, cost)
		}
	}
	if _, hit, lerr := e.store.LastRotateAtForAlt(ctx, bestOpp.ID, since); lerr != nil {
		return fail("pair cooldown query err %v（fail-closed）", lerr)
	} else if hit {
		return fail("组合对 24h 冷却中（cur=%.2f alt=%.2f cost=%.2f）", curEV, bestAltEV, cost)
	}

	// ⑥目标 venue 资金前置（R0-B1 防踏空：数据源 D-405，缺→fail-closed 不轮转）。
	if e.service == nil {
		return fail("资金检查器未接线（fail-closed）")
	}
	for _, vk := range []string{bestSpec.VenueBuy, bestSpec.VenueSell} {
		okF, known, d := e.service.MarginFloorKnown(ctx, vk, notional)
		if !known {
			return fail("目标 venue %s 资金数据缺（%s，fail-closed）", vk, d)
		}
		if !okF {
			return fail("目标 venue %s 资金不足（%s）", vk, d)
		}
	}

	// ⑦触发式：(altEV−curEV)/100 × notional × H/365 > switchCost × 2（H=30d 安全边际）。
	gain30d := (bestAltEV - curEV) / 100 * notional * rotateHorizonDays / 365
	out.altOppID = bestOpp.ID
	out.detail = fmt.Sprintf("cur=%.2f altEV=%.2f cost=%.2f gain30d=%.2f alt=%s",
		curEV, bestAltEV, cost, gain30d, bestOpp.ID)
	if gain30d > cost*rotateSafetyFactor {
		out.triggered = true
		return true, out
	}
	return false, out
}

// xassetComboNetCarry 假想替代组合的逐腿 carry 年化（%）——买腿 venueBuy/BUY/canonicalBuy +
// 卖腿 venueSell/SELL/canonicalSell，与 recalcXAssetNetCarry 同函数族（xassetLegCarryAnnual，
// 口径同源不双标）。任一腿缺 → ok=false。
func (e *Engine) xassetComboNetCarry(ctx context.Context, canonicalBuy, canonicalSell, venueBuy, venueSell string) (float64, bool) {
	maxAge := e.cfg.SwapFactMaxAge
	buy, ok := e.xassetLegCarryAnnual(ctx, venueBuy, canonicalBuy, "BUY", maxAge)
	if !ok {
		return 0, false
	}
	sell, ok := e.xassetLegCarryAnnual(ctx, venueSell, canonicalSell, "SELL", maxAge)
	if !ok {
		return 0, false
	}
	return buy + sell, true
}

// rotateSwitchCostUSD 换仓成本（USD）：双腿往返交易费 + 划转费常量。
// 费率源：service.VenueCommissionBps（commissionResolver 实测链）→ 缺/0 回退常量表
// （CEX taker 5bps / MT 0.3bps，SSOT S3 禁静默零值）。划转费=TRC20 $1 常量（§0/N2）。
func (e *Engine) rotateSwitchCostUSD(venueBuy, venueSell string, notional float64) float64 {
	feeBps := func(vk string) float64 {
		if e.service != nil {
			if v := e.service.VenueCommissionBps(vk); v > 0 {
				return v
			}
		}
		if strings.HasSuffix(vk, ":perp") {
			return rotateCexFeeBpsDflt
		}
		return rotateMtFeeBpsDflt
	}
	// 双腿 × 往返（平旧+开新各一次双边）= 4×单腿费率 ×名义。
	tradeFee := (feeBps(venueBuy) + feeBps(venueSell)) * 2 * notional / 1e4
	return tradeFee + rotateXferFeeUSD
}
