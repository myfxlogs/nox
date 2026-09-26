package dashboard

import (
	"arb/internal/keydomain"
	"errors"
	"fmt"
	"log"
	"math"
	"strconv"
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
		if !keydomain.IsMTKey(leg.venueKey) {
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
// D-311 S4：跨 canonical——买腿用 found.Canonical、卖腿用 found.CanonicalSell
// （nil/空回退 found.Canonical）。修前双腿同 canonical：跨 canonical 卖腿报价永 unavailable；
// 同所跨 canonical（买 XAUTUSDT 卖 PAXGUSDT @binance:perp）更糟——卖腿静默建成 XAUTUSDT
// 同码错腿（双腿同 symbol 自对冲+白烧手续费，且能过 validateLegQtys/freezeGuard）。
func (s *Service) resolveLegs(found *pgstore.Opportunity, qty float64) ([]exec.LegSpec, error) {
	sellCanonical := found.Canonical
	if found.CanonicalSell != nil && *found.CanonicalSell != "" {
		sellCanonical = *found.CanonicalSell
	}
	// D-481 普查：found.Canonical/CanonicalSell 即各侧 venue 采集键（detector 发射
	// MtCanonical/CexCanonical 分腿命名空间），canonical 即采集键非错位点。
	qbuy, okBuy := s.qbus.Latest(found.Canonical, found.VenueBuy)
	qsell, okSell := s.qbus.Latest(sellCanonical, found.VenueSell)
	if !okBuy || !okSell {
		return nil, errors.New("buy/sell leg quote unavailable")
	}
	// D-383 A2.4：执行解析 primary-only——MT 腿报价槽 VenueSymbol 须为 (venue,canonical)
	// 合格行（primary/隐式 primary）；多行组零 primary → 歧义态 fail-closed 拒单（宁拒单
	// 不可再撞车，497 LEGGED 实证）；报价槽符号 ≠ 合格行 → 陈旧/变体报价拒绝。
	// nil = 未接线（测试/CEX-only 部署）跳过校验。
	if s.primarySymbol != nil {
		for _, leg := range []struct{ venueKey, canonical, sym string }{
			{found.VenueBuy, found.Canonical, qbuy.VenueSymbol},
			{found.VenueSell, sellCanonical, qsell.VenueSymbol},
		} {
			if !keydomain.IsMTKey(leg.venueKey) {
				continue // CEX 腿无 broker_symbol_map 语义
			}
			primary, err := s.primarySymbol(leg.venueKey, leg.canonical)
			if err != nil {
				return nil, fmt.Errorf("resolve %s %s: %w", leg.venueKey, leg.canonical, err)
			}
			if primary != leg.sym {
				return nil, fmt.Errorf("quote venue symbol %s != primary %s for %s %s (reject: stale/variant quote)", leg.sym, primary, leg.venueKey, leg.canonical)
			}
		}
	}
	csBuy, err := s.legContractSizeOf(found.VenueBuy, qbuy.VenueSymbol)
	if err != nil {
		return nil, err
	}
	csSell, err := s.legContractSizeOf(found.VenueSell, qsell.VenueSymbol)
	if err != nil {
		return nil, err
	}
	// D-329：腿名义两段式匹配（pass-1 原样 + pass-2 粗腿锚定，见 service_legs_match.go）——
	// CLUSDT 型结构性失衡（粗腿步长 > 名义粒度带）从恒 abort 改为锚定匹配放行/语义化 fail-fast。
	buyQty, sellQty, err := s.matchLegsNotional(found.VenueBuy, qbuy, csBuy, found.VenueSell, qsell, csSell, qty)
	if err != nil {
		return nil, err
	}
	// 量规格预检（D-061）：双腿 qty 不得低于 venue 最小手数（position #3 实证形态）。
	if err := s.checkLegVolume(found.VenueBuy, qbuy.VenueSymbol, buyQty); err != nil {
		return nil, err
	}
	if err := s.checkLegVolume(found.VenueSell, qsell.VenueSymbol, sellQty); err != nil {
		return nil, err
	}
	return []exec.LegSpec{
		{VenueKey: found.VenueBuy, VenueSymbol: qbuy.VenueSymbol, Side: "BUY", Qty: buyQty, PriceExpected: qbuy.Ask},
		{VenueKey: found.VenueSell, VenueSymbol: qsell.VenueSymbol, Side: "SELL", Qty: sellQty, PriceExpected: qsell.Bid},
	}, nil
}

// checkLegVolume D-061 量规格预检：qty < venue 最小手数 → 拒（下单前，防单腿被拒 LEGGED）。
// CEX 腿跳过；解析器未接线/未知 → 放行（服务端拒单兜底）。
func (s *Service) checkLegVolume(venueKey, venueSymbol string, qty float64) error {
	if !keydomain.IsMTKey(venueKey) {
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

// legContractSizeOf 单腿合约规格：CEX :spot 腿 CS=1（qty 即基础币数）；CEX :perp 腿经
// cexContractSize 注入查 fin_cex_universe.ct_val（D-311 S3，gate=quanto_multiplier/okx=ctVal/
// binance=1.0——旧版恒返 1.0，gate/okx 腿 qty/名义错 10~1000 倍）；MT 腿经 legContractSize
// 注入查询（main 接 broker_symbol_map.contract_size，D-042 backfilled）。缺失/未接线 →
// 报错拒不猜（fail-safe，防 position #2 形态复发）。
func (s *Service) legContractSizeOf(venueKey, venueSymbol string) (float64, error) {
	if keydomain.IsType(venueKey, "perp") {
		if s.cexContractSize == nil {
			return 0, errors.New("cex contract size resolver not wired")
		}
		venue, _, _ := strings.Cut(venueKey, ":")
		cs, ok := s.cexContractSize(venue, venueSymbol)
		if !ok || cs <= 0 {
			return 0, fmt.Errorf("contract size unknown for %s %s (reject: notional derivation unavailable)", venueKey, venueSymbol)
		}
		return cs, nil
	}
	if !keydomain.IsMTKey(venueKey) {
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
	return keydomain.IsMTKey(venue)
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
// 步长分派下沉至 legStepQty（D-329 提取复用）：MT 腿取整后为 0 回退 1 step（最小可交易量）；
// CEX 腿按品种 stepSize 取整，D-127 P2 起未知品种返回 0（fail-closed，由 P3 预检拒单）。
func (s *Service) floorToStepQty(venueKey, symbol string, qty float64) float64 {
	step := s.legStepQty(venueKey, symbol)
	if isMTVenue(venueKey) {
		result := floorToStep(qty, step)
		if result <= 0 {
			result = step // 回退到最小可交易量 1 step
		}
		log.Printf("[exec] floorToStepQty mt %s %s qty=%.6f step=%.4f → %.6f", venueKey, symbol, qty, step, result)
		return result
	}
	if step <= 0 {
		log.Printf("[exec] floorToStepQty cex %s %s qty=%.6f step=0 → 0 (fail-closed, unknown symbol)", venueKey, symbol, qty)
		return 0
	}
	result := floorToStep(qty, step)
	if qty > 0 && result <= 0 {
		// D-433：请求量>0 被取整抹零——归因=qty<step 低于 venue 最小张数（非未知品种）。
		log.Printf("[exec] floorToStepQty cex %s %s qty=%.6f step=%.6f → 0 (qty<step 抹零，低于venue最小张数)", venueKey, symbol, qty, step)
		return 0
	}
	log.Printf("[exec] floorToStepQty cex %s %s qty=%.6f step=%.6f → %.6f", venueKey, symbol, qty, step, result)
	return result
}

// floorToStep 纯函数：向下取整 qty 到 step 的整数倍（D-127 P1）。
// 修复两个浮点伪影：
//   - 缺陷①乘法伪影：math.Floor(1.400639/0.01)*0.01 = 1.4000000000000001（17 位序列化 → binance -1111）。
//     解法：按 step 小数位 round 回正——140*0.01*100=140.000…03 → Round(140.000…03)=140 → 140/100=1.4。
//   - 缺陷③除法下溢：1137.4/0.01 = 113739.99999999999 → Floor=113739 → qty 静默缩水 0.01。
//     解法：epsilon 1e-9 加在除法结果上 → 113740.000… → Floor=113740。
//
// step<=0 → 返回 0（调用方 fail-closed）。
func floorToStep(qty, step float64) float64 {
	if step <= 0 || qty <= 0 {
		return 0
	}
	n := math.Floor(qty/step + 1e-9) // epsilon 防除法下溢
	dec := stepDecimals(step)        // 0.01→2, 0.1→1, 1→0
	p := math.Pow10(dec)
	return float64(math.Round(n*step*p)) / p // round 防乘法伪影
}

// stepDecimals 解析 step 的小数位数（0.01→2, 0.1→1, 1→0, 0.001→3）。
// 用 strconv 格式化解析，避免 log10 浮点误差。
func stepDecimals(step float64) int {
	s := strconv.FormatFloat(step, 'f', -1, 64)
	dot := strings.IndexByte(s, '.')
	if dot < 0 {
		return 0
	}
	return len(s) - dot - 1
}

// cexStepSize 返回 binance:perp 品种的 stepSize（向下取整单位）。
// D-466：只吃 stepMeta 运行时表（venue 原生 exchangeInfo）——硬编码 fallback 表已撤
// （业主令「不得硬编码」）；stepMeta nil/未命中 → 0（fail-closed，由 P3 预检拒单）。
func (s *Service) cexStepSize(symbol string) float64 {
	if s.stepMeta != nil {
		return s.stepMeta.step(symbol)
	}
	return 0
}
