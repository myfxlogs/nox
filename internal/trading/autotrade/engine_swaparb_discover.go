package autotrade

import (
	"context"
	"log"
	"sort"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// discoverSwapCombos 从 facts 表自动扫描所有跨账户 swap 组合。
// minNetSwapBps 从 env 读取（NOX_AUTOTRADE_MIN_NET_SWAP_BPS，默认 0.2）。
// swap facts 新鲜度门禁 fail-closed——now−f.Ts > SwapFactMaxAge → 跳过该 fact + 计数日志。
// 净 swap 评估改用 swapRateFor（实测优先 settled → spec 兜底 → none fail-closed），
// 删除规格直查旧路径（longFacts/shortFacts 直接遍历配对）。
// 候选枚举仍用 spec facts（swap_long/short）作 venue×symbol 索引——settled facts 不覆盖全 venue
// （冷启动 venue 无画像），spec 是"已知可配对 venue"的完整索引；净 swap 值由 swapRateFor 重算。
func (e *Engine) discoverSwapCombos(ctx context.Context) []swapComboCandidate {
	longFacts, err := e.store.LatestFactsByKind(ctx, fact.KindSwapLong)
	if err != nil || len(longFacts) == 0 {
		return nil
	}
	shortFacts, err := e.store.LatestFactsByKind(ctx, fact.KindSwapShort)
	if err != nil || len(shortFacts) == 0 {
		return nil
	}
	// 批量预取 product_group 映射（venue+canonical → product_group）。
	// fail-open：查询失败=空 map，配对放行（sameProductGroup 空值放行语义）。
	productGroupMap, err := e.store.ListProductGroups(ctx)
	if err != nil {
		log.Printf("[autotrade] SWAPARB ListProductGroups failed (fail-open): %v", err)
	}
	// broker 托管 venue 非 connected → facts 退出配对（停用即剔除，不等 facts
	// 自然过期——52993526 停用后 facts 残留窗口内仍刷配对的实证缺陷）。fail-open 先例同
	// productGroupMap；venue ∉ broker_accounts（CEX/adapter 自管）放行（pgstore.BrokerVenueDiscoverable）。
	brokerStatuses, err := e.store.ListBrokerAccountStatuses(ctx)
	if err != nil {
		log.Printf("[autotrade] SWAPARB ListBrokerAccountStatuses failed (fail-open): %v", err)
		brokerStatuses = nil
	}
	{
		kept := longFacts[:0]
		for _, f := range longFacts {
			if pgstore.BrokerVenueDiscoverable(brokerStatuses, f.Venue) {
				kept = append(kept, f)
			}
		}
		longFacts = kept
		kept = shortFacts[:0]
		for _, f := range shortFacts {
			if pgstore.BrokerVenueDiscoverable(brokerStatuses, f.Venue) {
				kept = append(kept, f)
			}
		}
		shortFacts = kept
		if len(longFacts) == 0 || len(shortFacts) == 0 {
			return nil
		}
	}
	now := time.Now()
	maxAge := e.cfg.SwapFactMaxAge
	var staleLong, staleShort int
	if maxAge > 0 {
		filtered := longFacts[:0]
		for _, f := range longFacts {
			if now.Sub(f.Ts) > maxAge {
				staleLong++
				continue
			}
			filtered = append(filtered, f)
		}
		longFacts = filtered
		filteredS := shortFacts[:0]
		for _, f := range shortFacts {
			if now.Sub(f.Ts) > maxAge {
				staleShort++
				continue
			}
			filteredS = append(filteredS, f)
		}
		shortFacts = filteredS
		if staleLong > 0 || staleShort > 0 {
			log.Printf("[autotrade] SWAPARB fact freshness gate: skipped %d long + %d short (maxAge=%s)",
				staleLong, staleShort, maxAge)
		}
		if len(longFacts) == 0 || len(shortFacts) == 0 {
			return nil
		}
	}
	type swapKey struct{ canonical, venue string }
	type swapEntry struct {
		value float64
		src   string // "mt5" / "mt4" / etc（D-077 修复⑪B N2）
	}
	shortMap := make(map[swapKey]swapEntry)
	for _, f := range shortFacts {
		shortMap[swapKey{f.Symbol, f.Venue}] = swapEntry{value: f.Value, src: f.Src}
	}
	// 同类型配对安全红线——两腿 is_real 必须相等（杜绝实盘×demo 假对冲）。
	// 查询失败 → 空 map（fail-closed：所有组合不配对，isreal.Pair failOpen=false 对空 map 返回 false）。
	realMap, _ := e.store.ListAccountIsReal(ctx)
	var combos []swapComboCandidate
	minNetSwapBps := e.cfg.MinNetSwapBps
	// 修复⑪B N5：删除 ==0 → 0.2 二次默认（config 已有 env 默认 0.2）。
	// 显式 0 = 全扫语义恢复（用户可通过 NOX_AUTOTRADE_MIN_NET_SWAP_BPS=0 关闭门槛）。
	for _, lf := range longFacts {
		// 删除 lf.Value<=0 单腿正 swap 过滤——套利看净 swap（long+short），
		// 非单腿。指数/商品/加密 CFD 的 swap_long 通常为负（broker 收过夜费），
		// 旧过滤挡住了所有非 FX 品种。净 swap 门槛由下方 netSwap < minNetSwapBps 守卫。
		for sfKey, sfEntry := range shortMap {
			if sfKey.canonical != lf.Symbol {
				continue
			}
			// 产品维度校验——同 canonical 但不同 product_group 不配对
			// （470 根因：FxPro BRENT cfd_spot × PXBT BRENT cfd_leverage）。
			longPG := productGroupMap[pgstore.VenueCanonKey{Venue: lf.Venue, Canonical: lf.Symbol}]
			shortPG := productGroupMap[pgstore.VenueCanonKey{Venue: sfKey.venue, Canonical: sfKey.canonical}]
			if !sameProductGroup(longPG, shortPG) {
				log.Printf("[autotrade] SWAPARB skip %s: product group mismatch (long=%s/%s, short=%s/%s)",
					lf.Symbol, lf.Venue, longPG, sfKey.venue, shortPG)
				continue
			}
			if sfKey.venue == lf.Venue {
				continue
			}
			// 同类型配对——实盘×实盘、demo×demo；跨类型跳过（假对冲事故语义）。
			if !isRealPair(realMap, lf.Venue, sfKey.venue) {
				continue
			}
			// 修复⑪B N2：用 fact.Src 拼 venueKey（mt4 账户也能进 swap 套利）
			vLong := lf.Venue + ":" + lf.Src
			vShort := sfKey.venue + ":" + sfEntry.src
			// 净 swap 改用 swapRateFor（实测优先 → spec 兜底 → none fail-closed）。
			// bare venue 从 venueKey 提取（冒号前）。
			bareLong := swapSrcFromVenueKey(vLong)
			bareShort := swapSrcFromVenueKey(vShort)
			netSwap, swapSrc := e.swapRateForNet(ctx, bareLong, bareShort, lf.Symbol)
			if swapSrc == swapSrcNone {
				// fail-closed：无 settled 也无 spec → 跳过（不裸信 0）
				continue
			}
			if netSwap < minNetSwapBps {
				continue
			}
			// CARRY 合并 SWAPARB 统一路径——netSwap 换算年化 ≥ CarryMinNetAnnualized → CARRY，否则 SWAPARB。
			// annualPct = netSwap(bps/日) × 365 / 100（与 funding_carry.go 旧 CarryDetector 同口径）。
			strategy := "SWAPARB"
			if e.cfg.CarryMinNetAnnualized > 0 && netSwap*365/100 >= e.cfg.CarryMinNetAnnualized {
				strategy = "CARRY"
			}
			// 基差 = (卖腿 bid − 买腿 ask) / 买腿 ask × 1e4（只展示不动排序）
			// e.quotes nil 时 basis=0（测试无 qbus 注入不 panic）
			basisBps := 0.0
			basisAvailable := false // D-169 P0：quotes nil 或任一腿报价缺失 → false（trySwapArb fail-closed）
			if e.quotes != nil {
				if qL, ok1 := e.quotes.Latest(lf.Symbol, vLong); ok1 {
					if qS, ok2 := e.quotes.Latest(sfKey.canonical, vShort); ok2 {
						if qL.Ask > 0 {
							basisBps = (qS.Bid - qL.Ask) / qL.Ask * 10000
							basisAvailable = true // D-169 P0：双腿报价齐
						}
					}
				}
			}
			combos = append(combos, swapComboCandidate{
				canonical:      lf.Symbol,
				vLong:          vLong,
				vShort:         vShort,
				netSwapBps:     netSwap,
				basisBps:       basisBps,
				basisAvailable: basisAvailable, // D-169 P0
				swapSrc:        swapSrc,        // D-180 S2：决策日志带 swap_src
				strategy:       strategy,       // D-187：SWAPARB 或 CARRY
			})
		}
	}
	sort.Slice(combos, func(i, j int) bool {
		return combos[i].netSwapBps > combos[j].netSwapBps
	})
	return combos
}
