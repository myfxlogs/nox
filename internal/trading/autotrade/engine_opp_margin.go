package autotrade

// engine_opp_margin.go D-010 S3：margin_usd 预算→单腿名义折算 + canonical 聚簇闸
// 顺序面（自 engine_opp.go 折分，行数门禁）。

import (
	"context"
	"fmt"
	"log"
	"math"

	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// xassetMarginGate D-010 S3：margin 模式预算折算（N=margin÷(fracB+fracS)）+
// canonical 聚簇 cap 前瞻闸。返回（margin 折算出的单腿名义, ok）——
// ok=false=静默跳过已 log（杠杆实读失败 fail-closed / canonical 超档）；
// 非 margin 行 legN=0（调用方维持 notional 口径 sizing）。
func (e *Engine) xassetMarginGate(ctx context.Context, opp pgstore.Opportunity, sellCanonical string, spec *exec.XAssetComboAllowSpec) (float64, bool) {
	marginLegN := 0.0
	if spec.MarginUSD != nil {
		// margin_usd 预算→单腿名义 N=margin÷(fracB+fracS)；frac 实读缺一 →
		// fail-closed 跳过（杠杆实读失败保证金不可折算，不猜杠杆放行）。
		n, ok := e.marginLegNotional(ctx, opp, sellCanonical, spec)
		if !ok {
			log.Printf("[autotrade] skip %s: margin_usd 预算杠杆实读失败保证金不可折算（fail-closed 拒开）", opp.ID)
			return 0, false
		}
		marginLegN = n
	}
	// canonical 聚簇 cap 前瞻闸（判据体在 engine_opp_capgate.go）。
	if e.canonicalCapRejectLog(ctx, opp, sellCanonical, spec, marginLegN) {
		return 0, false
	}
	return marginLegN, true
}

// marginLegNotional D-010 S3：margin_usd 预算→单腿名义 N=margin÷(fracB+fracS)。
// frac 经 engineService.LegMarginFrac 实读（venue 原生符号=qbus canonical→VenueSymbol
// 解析，同 dashboard comboPoolVerdict 法）；任一不可得→ok=false（fail-closed）。
func (e *Engine) marginLegNotional(ctx context.Context, opp pgstore.Opportunity, sellCanonical string, spec *exec.XAssetComboAllowSpec) (float64, bool) {
	if e.service == nil {
		return 0, false
	}
	symOf := func(canon, vk string) string {
		if e.quotes != nil {
			if q, ok := e.quotes.Latest(canon, vk); ok {
				return q.VenueSymbol
			}
		}
		return ""
	}
	fracB, okB := e.service.LegMarginFrac(ctx, spec.VenueBuy, symOf(opp.Canonical, spec.VenueBuy), false)
	fracS, okS := e.service.LegMarginFrac(ctx, spec.VenueSell, symOf(sellCanonical, spec.VenueSell), true)
	if !okB || !okS || fracB+fracS <= 0 {
		return 0, false
	}
	return *spec.MarginUSD / (fracB + fracS), true
}

// resolveXAssetSizing D-581：pct/system 档 armed 进场径活池折算——gross=
// pct×min(双腿池权益)（AccountEquityUSDAt 活快照；equity 口径保守不放大）。
// pct=行原样落表值；system=app_config grad_system_alloc_pct（决策席域判据）。
// 池缺/非正 → 条件请求落 fin_condition_gap（kind=capital，缺资自动化）+拒开。
func (e *Engine) resolveXAssetSizing(ctx context.Context, opp pgstore.Opportunity, spec *exec.XAssetComboAllowSpec) bool {
	if spec == nil || spec.SizingMode == "" || spec.SizingMode == "fixed" {
		return true
	}
	pct := 0.0
	if spec.SizingMode == "pct" && spec.FundingPct != nil {
		pct = *spec.FundingPct
	}
	if spec.SizingMode == "system" {
		if v, ok, err := e.store.AppConfigFloat(ctx, "grad_system_alloc_pct"); err == nil && ok {
			pct = v
		}
	}
	if !(pct > 0) {
		log.Printf("[autotrade] skip %s: sizing_mode=%s pct 不可得（fail-closed）", opp.ID, spec.SizingMode)
		return false
	}
	eqB, curB, _, okB := e.service.AccountEquityUSDAt(spec.VenueBuy)
	eqS, curS, _, okS := e.service.AccountEquityUSDAt(spec.VenueSell)
	usdLike := func(c string) bool { return c == "USD" || c == "USDT" || c == "UST" }
	if okB && !usdLike(curB) || okS && !usdLike(curS) {
		log.Printf("[autotrade] skip %s: sizing %s 池币种非 USD 系（%s/%s，fail-closed 不猜汇率）", opp.ID, spec.SizingMode, curB, curS)
		return false
	}
	if !okB || !okS {
		log.Printf("[autotrade] skip %s: sizing %s 腿池权益读缺（%s ok=%v %s ok=%v）", opp.ID, spec.SizingMode, spec.VenueBuy, okB, spec.VenueSell, okS)
		return false
	}
	base := math.Min(eqB, eqS)
	jitGapKind := ""
	if base <= 0 && e.JitSweepFn != nil {
		// JIT 归集前置（划转执行件）：缺资腿 earn 桶赎缺口额→交易户→重读
		// 权益原路 sizing；归集败/仍非正才落业主请求箱。败因 kind=落箱分类
		// （permission=授权面 key scope 缺——票面②）。
		for _, lg := range []struct {
			vk        string
			eq, other float64
		}{{spec.VenueBuy, eqB, eqS}, {spec.VenueSell, eqS, eqB}} {
			if lg.eq > 0 || lg.other <= 0 {
				continue
			}
			kind, err := e.JitSweepFn(ctx, lg.vk, lg.other-lg.eq, opp.NetBps/10000)
			if err != nil {
				log.Printf("[autotrade] jit 归集 %s 缺口 %.2f 败（%s 落箱面）: %v", lg.vk, lg.other-lg.eq, kind, err)
				jitGapKind = kind
				continue
			}
			e.service.RefreshAccountEquity(ctx, lg.vk)
			if v, _, _, ok := e.service.AccountEquityUSDAt(lg.vk); ok && v > 0 {
				if lg.vk == spec.VenueBuy {
					eqB = v
				} else {
					eqS = v
				}
			}
		}
		base = math.Min(eqB, eqS)
	}
	if base <= 0 {
		// 池短fall → 条件请求自动化（D-581④）：armed 组合缺资机制化落箱。
		gapKind := "capital"
		if jitGapKind == "permission" {
			gapKind = "permission" // jit 归集败因=授权面→permission 型请求箱（票面②）
		}
		if id, err := e.store.OpenConditionGap(ctx, &pgstore.ConditionGap{
			TemplateID: "xasset_combo", Kind: gapKind,
			Subject: spec.VenueBuy + "|" + spec.VenueSell,
			RequestText: fmt.Sprintf("毕业组合 armed 腿池缺资（%s=%.2f/%s=%.2f，sizing_mode=%s）——注资/装配后活池折算自动生效",
				spec.VenueBuy, eqB, spec.VenueSell, eqS, spec.SizingMode)}); err == nil {
			_ = e.store.RequestConditionGap(ctx, id)
		}
		log.Printf("[autotrade] skip %s: sizing %s 池净值非正（%.2f/%.2f，条件请求已落）", opp.ID, spec.SizingMode, eqB, eqS)
		return false
	}
	exec.XAssetSpecApplyPoolCaps(spec, base*pct)
	return true
}
