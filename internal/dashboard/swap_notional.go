package dashboard

import (
	"fmt"
)

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

// SwapMinNotionalUSD 计算 swap 套利组合的最小可交易名义额（D-072 §3.3）。
// = max(双腿 floorToStepQtyMin × CS × price)——双腿中要求更大名义额的那条决定下限。
// 返回 ok=false 表示报价不可用或合约规格未知。
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
	notionalLong := minQtyLong * csLong * qLong.Ask
	notionalShort := minQtyShort * csShort * qShort.Bid
	if notionalLong > notionalShort {
		return notionalLong, true
	}
	return notionalShort, true
}

// SwapArbNotional 从 broker 余额推导 swap 套利名义额（D-072 §3.3，业主不限额决策；S6 加 ratio 系数）。
// notional = min(equityLong, equityShort) × ratio（ratio = ARB_SWAPARB_EQUITY_USE_RATIO，缺省 0.5）；
// 不限额=无绝对上限；ratio=单组合资金使用率，保留抗单余地（单腿裸露承受力）。
// ratio=0（未接线测试）等价 ratio=1 旧行为；ratio=1 等价旧行为（100% 资金使用率）。
// fail-closed 三条件：① venue 离线 ② 账户币种非 USD ③ min(equity) < SwapMinNotionalUSD
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
	if minEq < minNotional {
		return 0, false, fmt.Sprintf("balance $%.2f < min notional $%.2f", minEq, minNotional)
	}
	// S6：ratio 系数（缺省 0.5，保留抗单余地）；0 = 未接线测试等价 ratio=1
	return minEq * swapArbRatio(s.swapArbEquityUseRatio), true, ""
}

// swapArbRatio 返回有效 ratio（0 或 >1 钳到 1，等价旧行为）。
// 语义：不限额=无绝对上限；ratio=单组合资金使用率，保留抗单余地（单腿裸露承受力）。
func swapArbRatio(r float64) float64 {
	if r <= 0 || r > 1 {
		return 1
	}
	return r
}
