package exec

import (
	"context"
	"fmt"
	"strings"

	"nox/internal/platform/combo"
	"nox/internal/platform/notional"
)

// SetRevalidateThresholds 设置复核劣化双阈值（D-077 修复⑪B N4，env 化）。
func (p *Pipeline) SetRevalidateThresholds(absBps, relPct float64) {
	p.revalidateAbsBps = absBps
	p.revalidateRelPct = relPct
}

// SetMTDisasterSlPct 注入 MT 灾难 SL 距离百分比（D-146 S1，NOX_MT_DISASTER_SL_PCT）。
// ≤0 = 禁用（MT 腿不挂 SL，回退现行为）。main 装配后调。
func (p *Pipeline) SetMTDisasterSlPct(pct float64) { p.mtDisasterSlPct = pct }

// SetPositionVerifier 注入平仓后回读器（D-077 修复⑩ G1）。main 装配后调。
func (p *Pipeline) SetPositionVerifier(v PositionVerifier) { p.verifier = v }

// SetContractSizeResolver 注入合约规格查询器（D-077 修复⑪A M1）。main 装配后调。
func (p *Pipeline) SetContractSizeResolver(r ContractSizeResolver) { p.csResolver = r }

// SetPnLCalibrator 注入历史单权威 PnL 回读器（D-077 修复⑪A M1）。main 装配后调。
func (p *Pipeline) SetPnLCalibrator(c PnLCalibrator) { p.pnlCalibrator = c }

// SetFxResolver 注入计价币→USD 换算器（D-105 T2）。main 装配后调；*pgstore.Store 满足接口。
func (p *Pipeline) SetFxResolver(r FxResolver) { p.fxResolver = r }

// SetSwapArbComboAllow 注入 SWAPARB 精确组合门禁 allowlist + notionalUSD（D-159 W1a R1 S2.3）。
// nil/空 = 未接线（gate 层 + dashboard 手动入口兜底，pipeline 不重复检查）。
func (p *Pipeline) SetSwapArbComboAllow(allow []ComboAllowSpec, notionalUSD float64) {
	p.swapArbComboAllow.Store(&allow) // D-515 S5：热刷新写侧（同 xassetAllow 模式）
	p.notionalUSD = notionalUSD
}

// swapArbAllowSpecs 热读读侧（nil=未接线——gate 层 fail-closed 兜底语义不动）。
func (p *Pipeline) swapArbAllowSpecs() []ComboAllowSpec {
	if v := p.swapArbComboAllow.Load(); v != nil {
		return *v
	}
	return nil
}

// SetXAssetComboAllow 注入 XASSET_CARRY 精确组合门禁 allowlist（D-311 S6）。
// 零信任：posType=XASSET_CARRY 时无条件校验（空表=全拒）。
func (p *Pipeline) SetXAssetComboAllow(allow []XAssetComboAllowSpec) {
	p.xassetAllow.Store(&allow) // D-403 R2：热刷新写侧
}

// xassetAllowSpecs D-403 R2：热刷新读侧（atomic Load；nil=未接线=空集全拒，零信任不破）。
func (p *Pipeline) xassetAllowSpecs() []XAssetComboAllowSpec {
	if v := p.xassetAllow.Load(); v != nil {
		return *v
	}
	return nil
}

// ReloadXAssetComboAllow D-403 R2：approve 三链扇出的 pipeline 写侧（整体替换快照，
// 免重启生效；与执行读侧 checkXAssetComboAllow 的 Load 竞态安全）。
func (p *Pipeline) ReloadXAssetComboAllow(specs []XAssetComboAllowSpec) {
	p.xassetAllow.Store(&specs)
}

// LegSpec 是单腿下单规格。
type LegSpec struct {
	VenueKey      string  // "venue:venueType"
	VenueSymbol   string  // venue 原生符号
	Side          string  // BUY / SELL
	Qty           float64 // :perp 腿=合约数（张），其余=基础币数量/MT 手数（D-311 R4：构造 exec.Order 时经 orderQtyFor ×cs 换算为基础币）
	PriceExpected float64 // 下单时本腿可执行价（买=ask 卖=bid，D-060 实施修正②：复盘 deviation 口径需要）
}

// ExecuteResult 是执行结果。
type ExecuteResult struct {
	PositionID int64
	Status     string // OPEN / LEGGED / FAILED
	Legs       []LegResult
}

// LegResult 是单腿成交结果。
type LegResult struct {
	VenueKey string
	OrderID  string
	Price    float64
	Qty      float64
	Status   string // FILLED / FAILED
	// Reason D-208①：venue 拒因原文（FAILED 时携带；落库限长截断 512B 由 persist 侧保证）。
	Reason     string
	Swap       float64
	Commission float64
	// FeeUSD D-391 S2：入场手续费（正数成本口径；0=venue 未回报）。
	FeeUSD float64
	// FillKind D-018 阶段a：成交方式（"maker"|"taker"；""=市价旧径）——落 position_legs.fill_kind。
	FillKind string
}

// checkSwapArbComboAllow D-159 W1a R1 S2.3：SWAPARB 精确组合门禁——真实名义额检查。
// (canonical, venueBuy, venueSell) 匹配 allowlist 后校验 qty/gross。
// 返修同修：venue 方向取双腿权威值（BUY 腿=venueLong，SELL 腿=venueShort）——
// ID 内 venue 对经 evaluator genID 字典序排序，解析会错向；SWAPARB 不在
// opportunities_type_check 内（唯一入口 ExecuteMeanRevertWithCap 的 combo.Key
// 合成 ID，永无机会行可查），legs 才是实际下单方向。canonical 段不排序，可解析。
// grossUSD = Σ(qty × CS × price × fx)（SSOT 要求真实名义额，非 notionalUSD 近似）。
// CS/FX 缺失 → fail-closed 拒绝（不回退 1.0）。
func (p *Pipeline) checkSwapArbComboAllow(ctx context.Context, opportunityID string, legs []LegSpec) error {
	comboKey := combo.FromOppID(opportunityID)
	allParts := strings.Split(comboKey, ":")
	if len(allParts) < 2 {
		return fmt.Errorf("%w: cannot parse combo key %q (want 2+ parts, got %d)", ErrComboNotAllowed, comboKey, len(allParts))
	}
	canonical := allParts[1]
	var venueBuy, venueSell string
	for _, leg := range legs {
		if leg.Side == "BUY" {
			venueBuy = leg.VenueKey
		} else if leg.Side == "SELL" {
			venueSell = leg.VenueKey
		}
	}
	if venueBuy == "" || venueSell == "" {
		return fmt.Errorf("%w: legs missing BUY/SELL venue for %q (fail-closed)", ErrComboNotAllowed, opportunityID)
	}
	spec := MatchComboAllow(p.swapArbAllowSpecs(), canonical, venueBuy, venueSell)
	if spec == nil {
		return fmt.Errorf("%w: %s %s×%s", ErrComboNotAllowed, canonical, venueBuy, venueSell)
	}
	var longQty, shortQty float64
	for _, leg := range legs {
		if leg.Side == "BUY" {
			longQty = leg.Qty
		} else if leg.Side == "SELL" {
			shortQty = leg.Qty
		}
	}
	if spec.MaxLongQty > 0 && longQty > spec.MaxLongQty {
		return fmt.Errorf("%w: long %.4f > %.4f", ErrQtyExceedsLimit, longQty, spec.MaxLongQty)
	}
	if spec.MaxShortQty > 0 && shortQty > spec.MaxShortQty {
		return fmt.Errorf("%w: short %.4f > %.4f", ErrQtyExceedsLimit, shortQty, spec.MaxShortQty)
	}
	// 真实名义额 = qty × CS × price × fx（SSOT 要求，非 notionalUSD 近似）。
	// CS/FX 缺失 → fail-closed 拒绝（不回退 1.0）。
	if spec.MaxGrossExposureUSD > 0 {
		quoteCcy := notional.QuoteCcy(canonical)
		var grossUSD float64
		for _, leg := range legs {
			cs := 1.0
			if p.csResolver != nil {
				v, ok := p.csResolver.ContractSize(leg.VenueKey, leg.VenueSymbol)
				if !ok || v <= 0 {
					return fmt.Errorf("%w: CS missing for %s %s (fail-closed)", ErrComboNotAllowed, leg.VenueKey, leg.VenueSymbol)
				}
				cs = v
			} else {
				return fmt.Errorf("%w: csResolver not wired (fail-closed)", ErrComboNotAllowed)
			}
			fx := 1.0
			if quoteCcy != "" && quoteCcy != "USD" {
				if p.fxResolver != nil {
					rate, ok := p.fxResolver.USDPerNow(ctx, quoteCcy, mtLegVenueKeyOf(legs))
					if !ok || rate <= 0 {
						return fmt.Errorf("%w: fx missing for %s (fail-closed)", ErrComboNotAllowed, quoteCcy)
					}
					fx = rate
				} else {
					return fmt.Errorf("%w: fxResolver not wired for non-USD quote ccy %s (fail-closed)", ErrComboNotAllowed, quoteCcy)
				}
			}
			grossUSD += leg.Qty * cs * leg.PriceExpected * fx
		}
		if grossUSD > spec.MaxGrossExposureUSD {
			return fmt.Errorf("%w: gross $%.2f > $%.2f", ErrGrossExposureExceeds, grossUSD, spec.MaxGrossExposureUSD)
		}
	}
	return nil
}

// checkXAssetComboAllow D-311 S6：XASSET_CARRY 精确组合门禁——零信任显式白名单。
// (canonicalBuy, canonicalSell, venueBuy, venueSell) 全部从 opportunities 行读——
// ID 内 venue 对经 evaluator genID 字典序排序，解析会错向（D-311 返修）；
// 行缺失/查询失败 → fail-closed 拒绝。canonical_sell NULL → 回退 canonicalBuy。
// 空表/不命中 → ErrComboNotAllowed；命中后校验双腿 qty 上限 + 真实名义额 gross USD
// 上限（CS/FX 缺失 fail-closed，与 checkSwapArbComboAllow 同语义；计价币按各腿自己的
// canonical 取）。
func (p *Pipeline) checkXAssetComboAllow(ctx context.Context, opportunityID string, legs []LegSpec) error {
	if p.store == nil {
		return fmt.Errorf("%w: store not wired (fail-closed)", ErrXAssetComboNotAllowed)
	}
	opp, err := p.store.GetOpportunity(ctx, opportunityID)
	if err != nil || opp == nil {
		return fmt.Errorf("%w: opportunity %q lookup failed (fail-closed, err=%v)", ErrXAssetComboNotAllowed, opportunityID, err)
	}
	canonicalBuy := opp.Canonical
	venueBuy, venueSell := opp.VenueBuy, opp.VenueSell
	canonicalSell := canonicalBuy
	if opp.CanonicalSell != nil && *opp.CanonicalSell != "" {
		canonicalSell = *opp.CanonicalSell
	}
	spec := MatchXAssetComboAllow(p.xassetAllowSpecs(), canonicalBuy, canonicalSell, venueBuy, venueSell)
	if spec == nil {
		return fmt.Errorf("%w: %s/%s %s×%s", ErrXAssetComboNotAllowed, canonicalBuy, canonicalSell, venueBuy, venueSell)
	}
	var longQty, shortQty float64
	for _, leg := range legs {
		if leg.Side == "BUY" {
			longQty = leg.Qty
		} else if leg.Side == "SELL" {
			shortQty = leg.Qty
		}
	}
	if spec.MaxLongQty > 0 && longQty > spec.MaxLongQty {
		return fmt.Errorf("%w: long %.4f > %.4f", ErrQtyExceedsLimit, longQty, spec.MaxLongQty)
	}
	if spec.MaxShortQty > 0 && shortQty > spec.MaxShortQty {
		return fmt.Errorf("%w: short %.4f > %.4f", ErrQtyExceedsLimit, shortQty, spec.MaxShortQty)
	}
	if spec.MaxGrossUSD > 0 {
		var grossUSD float64
		for _, leg := range legs {
			legCanonical := canonicalSell
			if leg.Side == "BUY" {
				legCanonical = canonicalBuy
			}
			quoteCcy := notional.QuoteCcy(legCanonical)
			if p.csResolver == nil {
				return fmt.Errorf("%w: csResolver not wired (fail-closed)", ErrComboNotAllowed)
			}
			cs, ok := p.csResolver.ContractSize(leg.VenueKey, leg.VenueSymbol)
			if !ok || cs <= 0 {
				return fmt.Errorf("%w: CS missing for %s %s (fail-closed)", ErrComboNotAllowed, leg.VenueKey, leg.VenueSymbol)
			}
			fx := 1.0
			if quoteCcy != "" && quoteCcy != "USD" {
				if p.fxResolver == nil {
					return fmt.Errorf("%w: fxResolver not wired for non-USD quote ccy %s (fail-closed)", ErrComboNotAllowed, quoteCcy)
				}
				rate, ok := p.fxResolver.USDPerNow(ctx, quoteCcy, mtLegVenueKeyOf(legs))
				if !ok || rate <= 0 {
					return fmt.Errorf("%w: fx missing for %s (fail-closed)", ErrComboNotAllowed, quoteCcy)
				}
				fx = rate
			}
			grossUSD += leg.Qty * cs * leg.PriceExpected * fx
		}
		if grossUSD > spec.MaxGrossUSD {
			return fmt.Errorf("%w: gross $%.2f > $%.2f", ErrGrossExposureExceeds, grossUSD, spec.MaxGrossUSD)
		}
	}
	return nil
}

// XAssetAllowSpecs D-403 R2：白名单快照公开读口（扇出后核对/测试用；语义同内部读侧）。
func (p *Pipeline) XAssetAllowSpecs() []XAssetComboAllowSpec { return p.xassetAllowSpecs() }

// ReloadSwapArbComboAllow D-515 S5：表化热刷新写侧（整体替换快照；notionalUSD 不动）。
func (p *Pipeline) ReloadSwapArbComboAllow(allow []ComboAllowSpec) {
	p.swapArbComboAllow.Store(&allow)
}
