// engine_fee_amortize.go —— D-409：收割判定的全账净额感知（费用摊销门槛）。
// harvestCheck 命中收割线后、触发 close 前追加：平仓后全账净额为负且 carry 仍在
// 积累且摊平有望 → 延迟收割（decision=hold_fee_amortize 观测行），直到净额转正、
// 无望摊平（estDays 超上限）或延迟超首行时钟。
// 作用域限定：仅 anchor<0 域 + 真腿路径（found/usable）启用；代理路径（旧仓
// 尺度不同源）不挂门槛（detail 标 legacy_path）。缺数据=不延迟（R0-B3）。
package autotrade

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// feeAmortizeGate D-409 S1/§3.1：返回 hold=延迟收割；detail=观测行 detail 片段
// （hold 时全值落行；放行时仅标记位 detail，并入 harvest reason）。
// 调用前置：harvestCheck 已命中收割线（真腿路径）。
func (e *Engine) feeAmortizeGate(ctx context.Context, p *pgstore.Position, pairKey string) (bool, string) {
	// 前置：anchor<0 域限定（anchor>=0 倒挂域语义未定不套公式）+ 三要素齐。
	if p.ExpectedEntrySpreadBps == nil || *p.ExpectedEntrySpreadBps >= 0 ||
		p.EntryHarvestFloorBps == nil || p.OpenedAt == nil {
		return false, ""
	}
	anchor := *p.ExpectedEntrySpreadBps
	// 真腿路径复核：monitor 行缺/超窗（found=false）=代理路径 → 不挂门槛；
	// 报价缺/超龄（usable=false）时不延迟（收割线本不该命中，双保险 fail-open 放行）。
	basis, _, _, found, usable := e.xassetTrueLegLine(ctx, p, pairKey)
	if !found {
		return false, "legacy_path"
	}
	if !usable {
		return false, ""
	}

	legs, err := e.store.ListPositionLegs(ctx, p.ID)
	if err != nil {
		return false, "legs_unknown"
	}
	feeBps := e.cfg.XAssetCexFeeBps // enrich 同源同值（service_positions_enrich.go 先例）
	buyNotional, feesPaid, estExit, ok := xassetLegFeeLedger(legs, e.cfg.LegContractSizeResolver, feeBps)
	if !ok {
		return false, "legs_unknown" // 腿价缺/无买腿 → 缺数据不延迟
	}
	// R0-B2：方向无关差值式 basis−anchor（过冲域 |anchor|−|basis| 符号破产）；
	// bps→USD 分母=买腿名义额（anchor 自身分母=buy 价同口径）。
	captureUSD := (basis - anchor) * buyNotional / 10000
	accrued, err := xassetAccruedCarryUSD(ctx, e.store, p.ID)
	if err != nil {
		return false, "cashflows_unknown"
	}
	netIfClosedNow := accrued + captureUSD - feesPaid - estExit

	curNetCarry, carryOK := e.recalcXAssetNetCarry(ctx, p)
	capDays := e.cfg.XAssetFeeHoldMaxDays
	if capDays <= 0 {
		capDays = 2.0 // 缺省 48h 防呆天花板（env 未配回退）
	}
	// estDays=摊平所需天数（qtyUSD=买腿名义额，与 capture 分母同口径；carry 未命中时给 NaN
	// 占位——verdict 先行短路不吃该值）。
	estDays := math.NaN()
	if carryOK && curNetCarry > 0 {
		estDays = math.Abs(netIfClosedNow) / (curNetCarry / 100 / 365 * buyNotional)
	}
	// 延迟时钟：首行 hold_fee_amortize 起算不重置；查询失败=缺数据不延迟。
	delaying, elapsed := false, time.Duration(0)
	if firstAt, hit, ferr := e.store.FirstFeeAmortizeEvalAt(ctx, p.ID); ferr != nil {
		return false, "clock_unknown"
	} else if hit {
		delaying, elapsed = true, time.Since(firstAt)
	}
	hold, flag := feeAmortizeVerdict(netIfClosedNow, curNetCarry, carryOK, estDays, capDays, delaying, elapsed)
	if !hold {
		return false, flag
	}
	return true, fmt.Sprintf("net=%.2f capture=%.2f fees=%.2f estExit=%.2f carry=%.2f%%/yr estDays=%.2f",
		netIfClosedNow, captureUSD, feesPaid, estExit, curNetCarry, estDays)
}

// feeAmortizeVerdict D-409 §3.1 纯判定核（S5 纯函数矩阵直测；序=SSOT 钉序）：
// 缺 carry→harvest(carry_unknown)；carry 死→harvest；净额非负→harvest；
// estDays 超上限→harvest(est_days_over)；延迟中超首行时钟→harvest(forced)；否则 hold。
func feeAmortizeVerdict(net, curNetCarry float64, carryOK bool, estDays, capDays float64, delaying bool, elapsed time.Duration) (bool, string) {
	switch {
	case !carryOK:
		return false, "carry_unknown" // R0-B3：缺数据=不延迟
	case curNetCarry <= 0:
		return false, "carry_dead" // carry 已死，不拖延亏损仓
	case net >= 0:
		return false, fmt.Sprintf("net=%.2f>=0", net)
	case estDays > capDays:
		return false, fmt.Sprintf("est_days_over=true estDays=%.2f>cap=%.1f", estDays, capDays)
	case delaying && elapsed > time.Duration(capDays*float64(24*time.Hour)):
		return false, fmt.Sprintf("forced=true elapsed=%.1fd>cap=%.1f", elapsed.Hours()/24, capDays)
	default:
		return true, ""
	}
}

// xassetLegFeeLedger D-409 §3.1：腿名义额+费用台账（买腿名义/已付费用/平仓费估算）。
// 腿 notional=EffectiveQty×PriceActual×cs（cs resolver 缺→1，CEX 腿正确 MT 腿回退同族）；
// fee_usd NULL 腿按 legNotional×feeBps/10000 估算（enrich 同族，禁静默零值禁第二口径）。
// ok=false=任一 OPEN 腿价缺/无 BUY 腿（缺数据 → 调用方不延迟）。
func xassetLegFeeLedger(legs []pgstore.PositionLeg, csResolver func(string, string) (float64, bool), feeBps float64) (buyNotional, feesPaid, estExit float64, ok bool) {
	for i := range legs {
		l := legs[i]
		if l.Phase != "OPEN" || l.Status != "FILLED" {
			continue
		}
		if l.PriceActual == nil || *l.PriceActual <= 0 {
			return 0, 0, 0, false
		}
		cs := 1.0
		if csResolver != nil {
			if r, rok := csResolver(l.Venue, l.Symbol); rok && r > 0 {
				cs = r
			}
		}
		notional := l.EffectiveQty() * *l.PriceActual * cs
		if l.Side == "BUY" {
			buyNotional += notional
		}
		if l.FeeUSD != nil {
			feesPaid += *l.FeeUSD
		} else {
			feesPaid += notional * feeBps / 10000
		}
		estExit += notional * feeBps / 10000
	}
	return buyNotional, feesPaid, estExit, buyNotional > 0
}

// xassetAccruedCarryUSD D-409 §3.1：本仓已落 cashflows 的 funding/swap 净额
// （actual 口径；commission 不算 carry——费用由 fee_usd 台账单侧计防双计）。
func xassetAccruedCarryUSD(ctx context.Context, store *pgstore.Store, posID int64) (float64, error) {
	cfs, err := store.ListCashflows(ctx, posID)
	if err != nil {
		return 0, err
	}
	var sum float64
	for _, c := range cfs {
		if c.Kind != "funding" && c.Kind != "swap" {
			continue
		}
		if c.Quality != nil && *c.Quality != "actual" {
			continue // estimated/unknown 不入实收（SumCashflowsNet 同口径）
		}
		sum += c.AmountUSD
	}
	return sum, nil
}

// xassetPairKey D-409：pairKey 解析（harvestCheck/rotateCheck 同款 CanonicalSell 优先）。
// ok=false=opportunity 行缺/查询失败（harvestCheck 原 early-return 语义保留）。
func (e *Engine) xassetPairKey(ctx context.Context, p *pgstore.Position) (string, bool) {
	opp, err := e.store.GetOpportunity(ctx, p.OpportunityID)
	if err != nil || opp == nil {
		return "", false
	}
	if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		return *opp.CanonicalSell + "-" + strings.TrimSuffix(opp.Canonical, "USD"), true
	}
	return canonicalFromOppID(p.OpportunityID), true
}
