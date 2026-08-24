package dashboard

import (
	"fmt"
	"strings"

	"arb/internal/bus"
)

// usdPerLot 返回 1 手合约的 USD 名义额（A 修复：swap 定尺寸币种换算 bug）。
//
// 报价计价币由 canonical 后缀判定：
//   - USD 计价（canonical 以 USDT/USD 结尾）→ CS × price（直接美元名义）；
//   - JPY 计价（canonical 以 JPY 结尾）→ CS × price ÷ USDJPY（日元名义换算美元，USDJPY 取 qbus 最新任一 venue 价）；
//   - 其他计价币（GBP/EUR 等未来扩展）→ 走对应 fx 对，未支持 → ok=false 跳过 + 日志（勿猜）。
//
// price 为该腿报价（bid/ask 由调用方决定）；qbus 用于查 fx 对（USDJPY 等）。
// ok=false 表示 fx 报价不可用 → fail-closed 跳过该组合（禁止用旧值/硬编码汇率）。
func usdPerLot(canonical string, price, contractSize float64, qbus *bus.QuoteBus) (float64, bool) {
	if contractSize <= 0 || price <= 0 {
		return 0, false
	}
	switch {
	case strings.HasSuffix(canonical, "USDT"), strings.HasSuffix(canonical, "USD"):
		// USD 计价：CS × price 直接美元名义
		return contractSize * price, true
	case strings.HasSuffix(canonical, "JPY"):
		// JPY 计价：CS × price ÷ USDJPY（日元名义换算美元）
		fx, ok := latestAnyVenue(qbus, "USDJPY")
		if !ok || fx <= 0 {
			return 0, false
		}
		return contractSize * price / fx, true
	default:
		// 其他计价币未支持 → fail-closed（勿猜）
		return 0, false
	}
}

// latestAnyVenue 从 qbus 取指定 canonical 的最新任一 venue 报价（mid 价）。
// 用于 fx 对换算（USDJPY 等，不限定 venue）。
func latestAnyVenue(qbus *bus.QuoteBus, canonical string) (float64, bool) {
	if qbus == nil {
		return 0, false
	}
	var best bus.Quote
	found := false
	for _, q := range qbus.AllLatest() {
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

// USDPerLotForLeg 返回指定腿（canonical + venueKey + price side）1 手的 USD 名义额。
// A 修复：供 autotrade.trySwapArb qty 定尺寸用（原 notional/ask 是 USD÷JPY 币种混算）。
// side="ask" 用买腿 ask 价，side="bid" 用卖腿 bid 价；fx 缺失 → ok=false fail-closed。
func (s *Service) USDPerLotForLeg(canonical, venueKey, side string) (float64, bool) {
	q, ok := s.qbus.Latest(canonical, venueKey)
	if !ok {
		return 0, false
	}
	cs, err := s.legContractSizeOf(venueKey, q.VenueSymbol)
	if err != nil {
		return 0, false
	}
	price := q.Ask
	if side == "bid" {
		price = q.Bid
	}
	return usdPerLot(canonical, price, cs, s.qbus)
}

// AccountEquityUSD 从 AccountMonitor 取指定 venue 的 Equity + Currency（D-072 §3.3）。
// 返回 ok=false 表示 venue 离线或未找到。调用方据此决定是否跳过组合。
func (s *Service) AccountEquityUSD(venueKey string) (value float64, currency string, ok bool) {
	if s.acctMon == nil {
		return 0, "", false
	}
	for _, a := range s.acctMon.Snapshot() {
		if a.VenueKey == venueKey {
			return a.Equity, a.Currency, a.Online
		}
	}
	return 0, "", false
}

// SwapMinNotionalUSD 计算 swap 套利组合的最小可交易名义额（D-072 §3.3，A 修复币种换算）。
// = max(双腿 floorToStepQtyMin × usdPerLot)——双腿中要求更大 USD 名义额的那条决定下限。
// usdPerLot 按计价币换算（JPY 对 ÷ USDJPY，USD 对直接乘）；fx 缺失 → ok=false fail-closed。
// 返回 ok=false 表示报价/合约规格/fx 不可用。
func (s *Service) SwapMinNotionalUSD(canonical, venueLong, venueShort string) (float64, bool) {
	qLong, okL := s.qbus.Latest(canonical, venueLong)
	qShort, okS := s.qbus.Latest(canonical, venueShort)
	if !okL || !okS {
		return 0, false
	}
	csLong, err := s.legContractSizeOf(venueLong, qLong.VenueSymbol)
	if err != nil {
		return 0, false
	}
	csShort, err := s.legContractSizeOf(venueShort, qShort.VenueSymbol)
	if err != nil {
		return 0, false
	}
	minQtyLong := s.floorToStepQtyMin(venueLong, qLong.VenueSymbol)
	minQtyShort := s.floorToStepQtyMin(venueShort, qShort.VenueSymbol)
	// A 修复：usdPerLot 按计价币换算（原 CS×price 是币种混算，JPY 对高估 ~150 倍）
	notionalLong, ok := usdPerLot(canonical, qLong.Ask, csLong, s.qbus)
	if !ok {
		return 0, false
	}
	notionalShort, ok := usdPerLot(canonical, qShort.Bid, csShort, s.qbus)
	if !ok {
		return 0, false
	}
	notionalLong *= minQtyLong
	notionalShort *= minQtyShort
	if notionalLong > notionalShort {
		return notionalLong, true
	}
	return notionalShort, true
}

// SwapArbNotional 从 broker 余额推导 swap 套利名义额（D-072 §3.3，业主不限额决策；S6 加 ratio 系数；D-097 R2 sizing 口径）。
// notional = min(equityLong, equityShort) × ratio（ratio = ARB_SWAPARB_EQUITY_USE_RATIO，缺省 0.5）；
// 不限额=无绝对上限；ratio=单组合资金使用率，保留抗单余地（单腿裸露承受力）。
// ratio=0（未接线测试）等价 ratio=1 旧行为；ratio=1 等价旧行为（100% 资金使用率）。
// D-097 R2：第三条 fail-closed 改 sizing 口径——minEq×ratio < minNotional → skip（缩放后名义至少够 1 个最小手）。
// D-097 R2：裸露 cap——notional > minEq×BareCap → cap 到 minEq×BareCap（只 cap 不 skip，防大权益+高 ratio 放大裸露）。
// fail-closed 三条件：① venue 离线 ② 账户币种非 USD ③ minEq×ratio < SwapMinNotionalUSD（sizing 不足）
// 返回 notional=0 + ok=false 表示跳过该组合（reason 含具体原因）。
func (s *Service) SwapArbNotional(canonical, venueLong, venueShort string) (notional float64, ok bool, reason string) {
	eqLong, curLong, onlineLong := s.AccountEquityUSD(venueLong)
	if !onlineLong {
		return 0, false, fmt.Sprintf("venue %s offline", venueLong)
	}
	eqShort, curShort, onlineShort := s.AccountEquityUSD(venueShort)
	if !onlineShort {
		return 0, false, fmt.Sprintf("venue %s offline", venueShort)
	}
	if curLong != "USD" {
		return 0, false, fmt.Sprintf("venue %s currency=%s (not USD, no FX converter)", venueLong, curLong)
	}
	if curShort != "USD" {
		return 0, false, fmt.Sprintf("venue %s currency=%s (not USD, no FX converter)", venueShort, curShort)
	}
	minEq := eqLong
	if eqShort < minEq {
		minEq = eqShort
	}
	minNotional, okMin := s.SwapMinNotionalUSD(canonical, venueLong, venueShort)
	if !okMin {
		return 0, false, "quote/contract size unavailable"
	}
	// D-097 R2：sizing 口径——minEq×ratio < minNotional → skip（缩放后名义至少够 1 个最小手）。
	ratio := swapArbRatio(s.swapArbEquityUseRatio)
	notional, okSizing := swapArbNotionalSizing(minEq, minNotional, ratio, s.swapArbBareCap)
	if !okSizing {
		return 0, false, fmt.Sprintf("sizing $%.2f < min lot notional $%.2f", minEq*ratio, minNotional)
	}
	return notional, true, ""
}

// swapArbNotionalSizing 纯函数：sizing 口径 + 裸露 cap（D-097 R2）。
// sizing = minEq×ratio；sizing < minNotional → ok=false（skip）；
// notional > minEq×bareCap → cap 到 minEq×bareCap（只 cap 不 skip）。
// bareCap≤0 = 不 cap（等价旧行为，无裸露上限）。
func swapArbNotionalSizing(minEq, minNotional, ratio, bareCap float64) (notional float64, ok bool) {
	sizing := minEq * ratio
	if sizing < minNotional {
		return 0, false
	}
	notional = sizing
	if cap := swapArbBareCapValue(bareCap); cap > 0 {
		bareLimit := minEq * cap
		if notional > bareLimit {
			notional = bareLimit
		}
	}
	return notional, true
}

// swapArbBareCapValue 返回有效 BareCap 系数。≤0 = 不 cap（等价旧行为，无裸露上限）。
func swapArbBareCapValue(c float64) float64 {
	if c <= 0 {
		return 0
	}
	return c
}

// swapArbRatio 返回有效 ratio（钳位到有效范围 0<x≤1）。
// 语义：不限额=无绝对上限；ratio=单组合资金使用率，保留抗单余地（单腿裸露承受力）。
// r≤0（未接线/误配）→ 1（等价旧行为，最保守=全资金）；r>1（误配放大）→ 1（钳上位）。
func swapArbRatio(r float64) float64 {
	if r <= 0 || r > 1 {
		return 1
	}
	return r
}
