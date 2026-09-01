// D-180 S2：swapRateFor helper——实测优先（settled 7d 中位数内部换算 bps）→ 规格（spec bps/日直接返回）→ none fail-closed。
// 接入 discoverSwapCombos/recalcNetSwap/S4 门禁；删除规格直查旧路径。
// 折扣（SpecDiscount）只作用 S4 入场门禁（source=spec 时 ×折扣），止损/展示用原值——防误杀存量仓。
package autotrade

import (
	"context"
	"log"
	"sort"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
)

// swapRateSource 取数来源标识（决策日志带 swap_src=settled|spec|none）。
const (
	swapSrcSettled = "settled"
	swapSrcSpec    = "spec"
	swapSrcNone    = "none"
)

// settledWindow D-180 S2：实测样本回看窗口（7 天，包级 var 便于测试覆写）。
var settledWindow = 7 * 24 * time.Hour

// swapRateFor D-180 S2：取指定 (venue, symbol, side) 的 swap 费率（bps/日）。
// 三态优先级：
//  1. settled（实测结算画像 swap_settled_long/short，7d 内中位数 USD/lot/day）→
//     内部换算 bps = usdLotDay / notionalPerLot × 10000，source=settled；
//     notionalPerLot = CS × mid（本 venue 报价 e.quotes.Latest；无报价 → 跳过 settled 走 spec/none）。
//  2. spec（规格 swap_long/short，已是 bps/日）→ 直接返回，source=spec；
//  3. none → (0, "none")，调用方 fail-closed。
//
// venue 为 bare venue（不含 :mt5 后缀）；side 为 "BUY"/"SELL"。
// 折扣（SpecDiscount）不在此处应用——helper 返回原值，折扣由 S4 门禁按 source 判定后乘。
func (e *Engine) swapRateFor(ctx context.Context, venue, symbol, side string) (float64, string) {
	kind := fact.KindSwapSettledLong
	if side != "BUY" {
		kind = fact.KindSwapSettledShort
	}
	// 1. settled 优先：7d 窗口内样本取中位数
	since := time.Now().Add(-settledWindow)
	settledFacts, err := e.store.FactsWithin(ctx, kind, venue, symbol, since)
	if err == nil && len(settledFacts) > 0 {
		values := make([]float64, 0, len(settledFacts))
		for _, f := range settledFacts {
			if f.Unit == fact.UnitUsdLotDay {
				values = append(values, f.Value)
			}
		}
		if len(values) > 0 {
			usdLotDay := medianFloat64(values)
			// 内部换算 bps = usdLotDay / notionalPerLot × 10000
			// notionalPerLot = CS × mid（本 venue 报价）
			notionalPerLot, ok := e.notionalPerLotForRate(venue, symbol)
			if ok && notionalPerLot > 0 {
				bps := usdLotDay / notionalPerLot * 10000
				return bps, swapSrcSettled
			}
			// 无报价 → 跳过 settled，走 spec 兜底（不虚构兜底源）
			log.Printf("[swapRateFor] settled hits but no quote for venue=%s symbol=%s → fallback spec",
				venue, symbol)
		}
	}
	// 2. spec 兜底：swap_long/short（已是 bps/日）
	specKind := fact.KindSwapLong
	if side != "BUY" {
		specKind = fact.KindSwapShort
	}
	specFacts, err := e.store.LatestFacts(ctx, specKind, venue, symbol)
	if err == nil && len(specFacts) > 0 {
		return specFacts[0].Value, swapSrcSpec
	}
	// 3. none fail-closed
	return 0, swapSrcNone
}

// notionalPerLotForRate D-180 S2：算 notionalPerLot（USD）= CS × mid × 计价币→USD 换算。
// D-183 修复：原实现 CS × mid 是计价币口径（JPY 对得 10M JPY 非 USD），settled USD/lot/day 换算 bps 时
// 分子 USD 分母 JPY 口径不一致 → bps 被低估 ~USDJPY 倍（AUDJPY 实证 0.005 vs 正确 0.75）。
// 修法：复用 usdPerLot 币种换算逻辑（JPY ÷ USDJPY，USD 直接，商品白名单，其他 fail-closed）。
// venue 为 bare venue（不含 :mt5 后缀），需拼回 venueKey 查报价。
// CS resolver 未注入/失败 → 用 CS=1（CEX 腿正确，MT 腿保守低估 notional → bps 偏高=保守）。
// 无报价/fx 缺失 → ok=false（调用方走 spec/none fail-closed）。
func (e *Engine) notionalPerLotForRate(venue, symbol string) (float64, bool) {
	if e.quotes == nil {
		return 0, false
	}
	// venueKey 拼接：尝试常见后缀（mt5/mt4）；spec fact.Src 含后缀信息但此处 bare venue 无后缀。
	// 实测：discoverSwapCombos 用 lf.Venue+":"+lf.Src 拼 venueKey，recalcNetSwap 用 leg.Venue（含后缀）。
	// 此处 venue 是 bare，需遍历 quotes 找匹配 venue 前缀的报价。
	// 简化：尝试 mt5 后缀（MT5 是主战场），未命中再尝试 mt4。
	for _, suffix := range []string{"mt5", "mt4", "spot", "perp"} {
		venueKey := venue + ":" + suffix
		if q, ok := e.quotes.Latest(symbol, venueKey); ok {
			mid := (q.Bid + q.Ask) / 2
			if mid <= 0 {
				continue
			}
			cs := 1.0
			if e.cfg.LegContractSizeResolver != nil {
				if resolved, ok := e.cfg.LegContractSizeResolver(venueKey, q.VenueSymbol); ok && resolved > 0 {
					cs = resolved
				}
			} else if e.service != nil {
				if resolver := e.service.LegContractSize(); resolver != nil {
					if resolved, ok := resolver(venueKey, q.VenueSymbol); ok && resolved > 0 {
						cs = resolved
					}
				}
			}
			// D-183：币种换算——CS × mid 是计价币口径，需转 USD。
			// JPY 对 ÷ USDJPY；USD/USDT/商品白名单直接；其他 fail-closed（勿猜）。
			return notionalPerLotUSD(symbol, cs, mid, e.quotes)
		}
	}
	return 0, false
}

// notionalPerLotUSD D-183：CS × price × 计价币→USD 换算（复用 dashboard.usdPerLot 逻辑）。
// JPY 对 → CS × price ÷ USDJPY；USD/USDT/商品白名单 → CS × price；其他 → fail-closed。
// fx 缺失 → ok=false（调用方走 spec/none）。
func notionalPerLotUSD(canonical string, cs, price float64, quotes *bus.QuoteBus) (float64, bool) {
	if cs <= 0 || price <= 0 {
		return 0, false
	}
	switch {
	case strings.HasSuffix(canonical, "USDT"), strings.HasSuffix(canonical, "USD"), usdQuotedCommodities[canonical]:
		return cs * price, true
	case strings.HasSuffix(canonical, "JPY"):
		fx, ok := latestAnyVenueQuote(quotes, "USDJPY")
		if !ok || fx <= 0 {
			return 0, false
		}
		return cs * price / fx, true
	default:
		return 0, false
	}
}

// latestAnyVenueQuote 从 QuoteBus 取指定 canonical 的最新任一 venue mid 价（fx 换算用）。
func latestAnyVenueQuote(quotes *bus.QuoteBus, canonical string) (float64, bool) {
	if quotes == nil {
		return 0, false
	}
	var best bus.Quote
	found := false
	for _, q := range quotes.AllLatest() {
		if q.Symbol == canonical {
			if !found || q.Time.After(best.Time) {
				best = q
				found = true
			}
		}
	}
	if !found {
		return 0, false
	}
	return (best.Bid + best.Ask) / 2, true
}

// usdQuotedCommodities D-183：USD 计价商品/指数 CFD 白名单（与 dashboard.usdPerLot 同源）。
var usdQuotedCommodities = map[string]bool{
	"BRENT": true, "WTI": true, "SILVER": true, "COPPER": true, "NATGAS": true,
	"XAUUSD": true, "XAGUSD": true, "XPDUSD": true, "XPTUSD": true,
	"NAS100": true, "SPX500": true, "US30": true, "US2000": true, "GER40": true,
	"UK100": true, "HK50": true, "JP225": true, "AUS200": true, "ES35": true,
	"FR40": true, "STOXX50": true,
}

// medianFloat64 中位数（排序后取中间值，偶数取两值平均）。
// 共用函数：swapRateFor settled 样本聚合用。
func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := make([]float64, len(values))
	copy(sorted, values)
	sort.Float64s(sorted)
	n := len(sorted)
	if n%2 == 1 {
		return sorted[n/2]
	}
	return (sorted[n/2-1] + sorted[n/2]) / 2
}

// swapRateForNet D-180 S2：取净 swap（long+short）bps + source。
// source 取两腿中较低优先级（settled>spec>none，任一 none → none fail-closed）。
// 折扣不在此应用（helper 返回原值，S4 门禁按 source 判定）。
func (e *Engine) swapRateForNet(ctx context.Context, venueLong, venueShort, symbol string) (float64, string) {
	longBps, longSrc := e.swapRateFor(ctx, venueLong, symbol, "BUY")
	shortBps, shortSrc := e.swapRateFor(ctx, venueShort, symbol, "SELL")
	// 任一 none → fail-closed
	if longSrc == swapSrcNone || shortSrc == swapSrcNone {
		return 0, swapSrcNone
	}
	net := longBps + shortBps
	// source 取较低优先级（settled>spec）：任一 spec → spec（保守）
	if longSrc == swapSrcSpec || shortSrc == swapSrcSpec {
		return net, swapSrcSpec
	}
	return net, swapSrcSettled
}

// swapSrcDiscount D-180 S4：spec 源折扣（仅入场门禁用，止损/展示用原值）。
// source=spec → netBps × SpecDiscount；settled → 原值；none → 0（fail-closed 已在上游处理）。
func swapSrcDiscount(netBps float64, source string, discount float64) float64 {
	if source == swapSrcSpec && discount > 0 && discount < 1 {
		return netBps * discount
	}
	return netBps
}

// swapSrcFromVenueKey 提取 bare venue（venueKey="venue:mt5" → "venue"）。
// 共用 helper（discoverSwapCombos/recalcNetSwap 调用 swapRateFor 时用）。
func swapSrcFromVenueKey(venueKey string) string {
	if idx := strings.Index(venueKey, ":"); idx > 0 {
		return venueKey[:idx]
	}
	return venueKey
}
