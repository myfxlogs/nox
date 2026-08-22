package dashboard

import (
	"errors"
	"fmt"
	"log"
	"math"
	"strings"

	"arb/internal/exec"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// checkTradability F5 执行预检：MT 腿（venueKey 后缀 :mt5/:mt4）查 trade_mode，
// 买腿 mode∉{LongOnly,Full} / 卖腿 mode∉{ShortOnly,Full} → 返回错误（含 venue+符号+mode），
// 调用方拒绝执行不建 position；CEX 腿跳过；未知（无 Fact/TTL 过期）放行（venue 服务端拒单是最终权威）。
func (s *Service) checkTradability(found *pgstore.Opportunity) error {
	if s.tradeModes == nil {
		return nil
	}
	for _, leg := range []struct {
		venueKey string
		buy      bool
	}{{found.VenueBuy, true}, {found.VenueSell, false}} {
		if !strings.HasSuffix(leg.venueKey, ":mt5") && !strings.HasSuffix(leg.venueKey, ":mt4") {
			continue // CEX 腿：trade_mode 仅 MT 语义
		}
		mode, ok := s.tradeModes(found.Canonical, leg.venueKey)
		if !ok {
			continue
		}
		if leg.buy && mode != fact.TradeModeLongOnly && mode != fact.TradeModeFull {
			return fmt.Errorf("buy leg %s %s not tradable (trade mode %d)", leg.venueKey, found.Canonical, mode)
		}
		if !leg.buy && mode != fact.TradeModeShortOnly && mode != fact.TradeModeFull {
			return fmt.Errorf("sell leg %s %s not tradable (trade mode %d)", leg.venueKey, found.Canonical, mode)
		}
	}
	return nil
}

// resolveLegs 从 qbus 取双腿 broker 原生符号构造下单规格（D-051 修②）。
// canonical 用机会列（非 ID 解析）；缺任一腿报价 → 返回错误（执行方 FAILED 透传）。
// D-060 实施修正②（D-028 补课）：卖腿等名义反算 sellQty = qty×CS_buy/CS_sell（0.01 取整）
// + 名义不平衡拒绝 + PriceExpected 落腿。实证：position #2 油 1000:100 桶/手同 qty 0.1
// 发卖腿被 IC 拒「Invalid volume」→ LEGGED（等名义反算 D-028 设计了但从未实现）。
func (s *Service) resolveLegs(found *pgstore.Opportunity, qty float64) ([]exec.LegSpec, error) {
	qbuy, okBuy := s.qbus.Latest(found.Canonical, found.VenueBuy)
	qsell, okSell := s.qbus.Latest(found.Canonical, found.VenueSell)
	if !okBuy || !okSell {
		return nil, errors.New("buy/sell leg quote unavailable")
	}
	csBuy, err := s.legContractSizeOf(found.VenueBuy, qbuy.VenueSymbol)
	if err != nil {
		return nil, err
	}
	csSell, err := s.legContractSizeOf(found.VenueSell, qsell.VenueSymbol)
	if err != nil {
		return nil, err
	}
	sellQty := math.Round(qty*csBuy/csSell*100) / 100
	// D-070：qty 精度对齐（Binance -1111 / MT5 Invalid volume 修复）
	// MT5 取整后为 0 会回退到 1 step（最小可交易量），不会返回 0
	qty = s.floorToStepQty(found.VenueBuy, qbuy.VenueSymbol, qty)
	sellQty = s.floorToStepQty(found.VenueSell, qsell.VenueSymbol, sellQty)
	// 不平衡口径修正：按买腿参考价统一计两腿名义——D-028 防的是「取整导致的数量差」，
	// 不是两腿各自可执行价的差（那是套利缺口本身：等量对冲腿在不同 venue 价格下名义额必然不同，
	// 3.3% 价差曾把正确订单误拒为 imbalance）。
	ref := qbuy.Ask
	notionalBuy := qty * csBuy * ref
	notionalSell := sellQty * csSell * ref
	maxImb := s.maxLegImbalance
	if maxImb <= 0 {
		maxImb = 2.0
	}
	imb := math.Abs(notionalSell-notionalBuy) / notionalBuy
	if notionalBuy <= 0 || imb > maxImb/100 {
		return nil, fmt.Errorf("leg notional imbalance: buy $%.2f vs sell $%.2f (%.2f%% > %.1f%%)",
			notionalBuy, notionalSell, imb*100, maxImb)
	}
	// 量规格预检（D-061）：双腿 qty 不得低于 venue 最小手数（position #3 实证形态）。
	if err := s.checkLegVolume(found.VenueBuy, qbuy.VenueSymbol, qty); err != nil {
		return nil, err
	}
	if err := s.checkLegVolume(found.VenueSell, qsell.VenueSymbol, sellQty); err != nil {
		return nil, err
	}
	return []exec.LegSpec{
		{VenueKey: found.VenueBuy, VenueSymbol: qbuy.VenueSymbol, Side: "BUY", Qty: qty, PriceExpected: qbuy.Ask},
		{VenueKey: found.VenueSell, VenueSymbol: qsell.VenueSymbol, Side: "SELL", Qty: sellQty, PriceExpected: qsell.Bid},
	}, nil
}

// checkLegVolume D-061 量规格预检：qty < venue 最小手数 → 拒（下单前，防单腿被拒 LEGGED）。
// CEX 腿跳过；解析器未接线/未知 → 放行（服务端拒单兜底）。
func (s *Service) checkLegVolume(venueKey, venueSymbol string, qty float64) error {
	if !strings.HasSuffix(venueKey, ":mt5") && !strings.HasSuffix(venueKey, ":mt4") {
		return nil
	}
	if s.legMinLots == nil {
		return nil
	}
	min, ok := s.legMinLots(venueKey, venueSymbol)
	if !ok || min <= 0 || qty >= min {
		return nil
	}
	return fmt.Errorf("qty %v below venue min lot %.2f for %s %s", qty, min, venueKey, venueSymbol)
}

// legContractSizeOf 单腿合约规格：CEX 腿 CS=1（qty 即基础币数）；MT 腿经 legContractSize
// 注入查询（main 接 broker_symbol_map.contract_size，D-042 backfilled）。缺失/未接线 →
// 报错拒不猜（fail-safe，防 position #2 形态复发）。
func (s *Service) legContractSizeOf(venueKey, venueSymbol string) (float64, error) {
	if !strings.HasSuffix(venueKey, ":mt5") && !strings.HasSuffix(venueKey, ":mt4") {
		return 1.0, nil
	}
	if s.legContractSize == nil {
		return 0, errors.New("leg contract size resolver not wired")
	}
	cs, ok := s.legContractSize(venueKey, venueSymbol)
	if !ok || cs <= 0 {
		return 0, fmt.Errorf("contract size unknown for %s %s (reject: notional derivation unavailable)", venueKey, venueSymbol)
	}
	return cs, nil
}

// isMTVenue 判断是否为 MT5/MT4 venue。
func isMTVenue(venue string) bool {
	return strings.HasSuffix(venue, ":mt5") || strings.HasSuffix(venue, ":mt4")
}

// floorToStepQtyMin 返回 MT5 品种的最小可交易量（1 个 volume step）。
func (s *Service) floorToStepQtyMin(venueKey, symbol string) float64 {
	if s.legVolumeStep != nil {
		if vs, ok := s.legVolumeStep(venueKey, symbol); ok && vs > 0 {
			return vs
		}
	}
	return 0.01
}

// QtyBelowMinStep H4.2：qty 低于 venue 最小可交易量（1 个 volume step）→ true。
// 供 autotrade 扫描入口静默跳过用（不产 intent 不落 REJECTED——品种适配事实非风控事件）。
// CEX 腿无 volume step 概念 → 返回 false（不跳过，由 floorToStepQty 兜底取整）。
// qty <= 0 → true（无效量跳过）。
func (s *Service) QtyBelowMinStep(venueKey, venueSymbol string, qty float64) bool {
	if qty <= 0 {
		return true
	}
	if !isMTVenue(venueKey) {
		return false // CEX 腿无 volume step 概念
	}
	return qty < s.floorToStepQtyMin(venueKey, venueSymbol)
}

// floorToStepQty 按品种精度向下取整 qty（D-070 修复 Binance -1111 / MT5 Invalid volume）。
// MT 腿：查 legVolumeStep（SymbolParams VolumeStep），未接线/未知用默认 0.01。
// D-070 CFD 适配：取整后为 0 则回退到 1 个 step（最小可交易量），防 qty=0 无法交易。
// CEX 腿：按品种 stepSize 取整；未知品种默认 0.01。
func (s *Service) floorToStepQty(venueKey, symbol string, qty float64) float64 {
	if strings.HasSuffix(venueKey, ":mt5") || strings.HasSuffix(venueKey, ":mt4") {
		step := 0.01 // 默认 MT5 手数步长
		if s.legVolumeStep != nil {
			if vs, ok := s.legVolumeStep(venueKey, symbol); ok && vs > 0 {
				step = vs
			}
		}
		result := math.Floor(qty/step) * step
		if result <= 0 {
			result = step // 回退到最小可交易量 1 step
		}
		log.Printf("[exec] floorToStepQty mt %s %s qty=%.6f step=%.4f → %.6f", venueKey, symbol, qty, step, result)
		return result
	}
	// CEX：按品种 stepSize
	step := cexStepSize(symbol)
	result := math.Floor(qty/step) * step
	log.Printf("[exec] floorToStepQty cex %s %s qty=%.6f step=%.6f → %.6f", venueKey, symbol, qty, step, result)
	return result
}

// cexStepSize 返回 CEX 品种的 stepSize（向下取整单位）。
// 已知品种用实测值；未知品种默认 0.01（2位小数，多数山寨币）。
func cexStepSize(symbol string) float64 {
	// 提取基础币种（去 USDT/USD 后缀）
	base := symbol
	for _, suf := range []string{"USDT", "USD", "-USDT", "-USD", "-SWAP"} {
		base = strings.TrimSuffix(base, suf)
	}
	switch base {
	case "BTC":
		return 0.001 // binance spot 0.00001, perp 0.001；取保守 0.001
	case "ETH":
		return 0.001 // binance spot 0.0001, perp 0.001
	case "TRX":
		return 1 // binance spot 1, perp 1
	case "SOL":
		return 0.01
	case "BNB":
		return 0.01
	case "XRP":
		return 1
	case "DOGE":
		return 1
	case "ADA":
		return 1
	case "MATIC":
		return 1
	case "DOT":
		return 0.01
	case "LINK":
		return 0.01
	case "LTC":
		return 0.001
	case "AVAX":
		return 0.01
	default:
		return 0.01 // 未知品种保守 2 位小数
	}
}
