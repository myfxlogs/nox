package dashboard

import (
	"fmt"
	"math"

	"arb/internal/usdnotional"
)

// USDPerLotForLeg 返回指定腿（canonical + venueKey + price side）1 手的 USD 名义额。
// A 修复：供 autotrade.trySwapArb qty 定尺寸用（原 notional/ask 是 USD÷JPY 币种混算）。
// side="ask" 用买腿 ask 价，side="bid" 用卖腿 bid 价；fx 缺失 → ok=false fail-closed。
// D-190：usdPerLot 逻辑迁移至 internal/usdnotional.PerLot（单一真相源，双拷贝清除）。
func (s *Service) USDPerLotForLeg(canonical, venueKey, side string) (float64, bool) {
	// D-481 普查：调用方为 swap 套利 MT 腿（trySwapArb），canonical=symbol map
	// 发射键即采集键；CEX 腿传入须为采集键形 canonical。
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
	return usdnotional.PerLot(canonical, price, cs, s.qbus)
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

// AccountLeverage 从 AccountMonitor 取指定 venue 的杠杆（D-142 ④ 杠杆不对称提示）。
// 返回 ok=false 表示 venue 离线/未找到/杠杆未知（0）。2e9=Exness 无限杠杆哨兵。
func (s *Service) AccountLeverage(venueKey string) (leverage float64, ok bool) {
	if s.acctMon == nil {
		return 0, false
	}
	for _, a := range s.acctMon.Snapshot() {
		if a.VenueKey == venueKey {
			return a.Leverage, a.Online && a.Leverage > 0
		}
	}
	return 0, false
}

// SwapMinNotionalUSD 计算 swap 套利组合的最小可交易名义额（D-072 §3.3，A 修复币种换算）。
// = max(双腿 floorToStepQtyMin × usdPerLot)——双腿中要求更大 USD 名义额的那条决定下限。
// usdPerLot 按计价币换算（JPY 对 ÷ USDJPY，USD 对直接乘）；fx 缺失 → ok=false fail-closed。
// 返回 ok=false 表示报价/合约规格/fx 不可用。
func (s *Service) SwapMinNotionalUSD(canonical, venueLong, venueShort string) (float64, bool) {
	// D-481 普查：同 USDPerLotForLeg——swap 套利 MT 双腿，canonical=采集键，非错位点。
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
	// D-190：usdPerLot 迁移至 usdnotional.PerLot
	notionalLong, ok := usdnotional.PerLot(canonical, qLong.Ask, csLong, s.qbus)
	if !ok {
		return 0, false
	}
	notionalShort, ok := usdnotional.PerLot(canonical, qShort.Bid, csShort, s.qbus)
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
	// D-174：ratio 语义从名义/权益改为保证金/权益，notional=maxMargin×leverage。
	ratio := swapArbRatio(s.swapArbEquityUseRatio)
	longLev, _ := s.AccountLeverage(venueLong)
	shortLev, _ := s.AccountLeverage(venueShort)
	leverage := effectiveLeverage(longLev, shortLev)
	notional, okSizing := swapArbNotionalSizing(minEq, minNotional, ratio, s.swapArbBareCap, leverage)
	if !okSizing {
		return 0, false, fmt.Sprintf("sizing $%.2f (margin $%.2f × lev %.0f) < min lot notional $%.2f", minEq*ratio*leverage, minEq*ratio, leverage, minNotional)
	}
	return notional, true, ""
}

// swapArbNotionalSizing 纯函数：sizing 口径 + 裸露 cap（D-174 杠杆感知）。
// ratio 语义=保证金/权益：maxMargin=minEq×ratio，maxNotional=maxMargin×leverage。
// maxNotional < minNotional → ok=false（skip）；
// notional > minEq×bareCap×leverage → cap（保证金口径，bareCap=保证金/权益比）。
// leverage=1.0（无杠杆回落）等价旧 D-097 R2 行为（向后兼容）。
// bareCap≤0 = 不 cap（等价旧行为，无裸露上限）。
func swapArbNotionalSizing(minEq, minNotional, ratio, bareCap, leverage float64) (notional float64, ok bool) {
	maxMargin := minEq * ratio
	maxNotional := maxMargin * leverage
	if maxNotional < minNotional {
		return 0, false
	}
	notional = maxNotional
	if cap := swapArbBareCapValue(bareCap); cap > 0 {
		bareNotionalLimit := minEq * cap * leverage
		if notional > bareNotionalLimit {
			notional = bareNotionalLimit
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

// effectiveLeverage D-174：取两腿较小杠杆（保守）。
// 无限杠杆哨兵 ≥1e6 → 视为无限，用另一腿；两腿都无限/未知 → 回落 1.0（最保守）。
// 杠杆≤0 = 未知，用另一腿；两腿都未知 → 回落 1.0。
func effectiveLeverage(longLev, shortLev float64) float64 {
	longInf := longLev >= 1e6
	shortInf := shortLev >= 1e6
	if longInf && shortInf {
		return 1.0
	}
	if longInf {
		return shortLev
	}
	if shortInf {
		return longLev
	}
	if longLev <= 0 && shortLev <= 0 {
		return 1.0
	}
	if longLev <= 0 {
		return shortLev
	}
	if shortLev <= 0 {
		return longLev
	}
	return math.Min(longLev, shortLev)
}
