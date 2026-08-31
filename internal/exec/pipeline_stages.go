package exec

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/combo"
	"arb/internal/notional"
)

// SetRevalidateThresholds 设置复核劣化双阈值（D-077 修复⑪B N4，env 化）。
func (p *Pipeline) SetRevalidateThresholds(absBps, relPct float64) {
	p.revalidateAbsBps = absBps
	p.revalidateRelPct = relPct
}

// SetMTDisasterSlPct 注入 MT 灾难 SL 距离百分比（D-146 S1，ARB_MT_DISASTER_SL_PCT）。
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
	p.swapArbComboAllow = allow
	p.notionalUSD = notionalUSD
}

// LegSpec 是单腿下单规格。
type LegSpec struct {
	VenueKey      string // "venue:venueType"
	VenueSymbol   string // venue 原生符号
	Side          string // BUY / SELL
	Qty           float64
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
	VenueKey   string
	OrderID    string
	Price      float64
	Qty        float64
	Status     string // FILLED / FAILED
	Swap       float64
	Commission float64
}

// checkSwapArbComboAllow D-159 W1a R1 S2.3：SWAPARB 精确组合门禁——真实名义额检查。
// 从 opportunityID 解析 (canonical, venueBuy, venueSell)，匹配 allowlist 后校验 qty/gross。
// grossUSD = Σ(qty × CS × price × fx)（SSOT 要求真实名义额，非 notionalUSD 近似）。
// CS/FX 缺失 → fail-closed 拒绝（不回退 1.0）；legs 中 BUY 腿=venueLong，SELL 腿=venueShort。
func (p *Pipeline) checkSwapArbComboAllow(ctx context.Context, opportunityID string, legs []LegSpec) error {
	comboKey := combo.FromOppID(opportunityID)
	// comboKey 格式 "STRATEGY:canonical:venueLong:venueShort"，venueKey 含 ":"（如 "acct:mt5"）。
	// SplitN(":",4) 会把 venueLong 截断 → 用全切分重组：strategy|canonical|acct|plat|acct|plat（6 段）。
	allParts := strings.Split(comboKey, ":")
	if len(allParts) < 6 {
		return fmt.Errorf("%w: cannot parse combo key %q (want 6+ parts, got %d)", ErrComboNotAllowed, comboKey, len(allParts))
	}
	canonical := allParts[1]
	venueBuy := allParts[2] + ":" + allParts[3]
	venueSell := allParts[4] + ":" + allParts[5]
	spec := MatchComboAllow(p.swapArbComboAllow, canonical, venueBuy, venueSell)
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
	// D-159 W1a R1 S2.3 返修：真实名义额 = qty × CS × price × fx（SSOT 要求，非 notionalUSD 近似）。
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
					rate, ok := p.fxResolver.USDPer(ctx, quoteCcy, time.Now())
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
