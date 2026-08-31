package autotrade

import (
	"context"
	"log"
	"sort"
	"time"

	"arb/internal/fact"
)

// discoverSwapCombos 从 facts 表自动扫描所有跨账户 swap 组合。
// D-072：minNetSwapBps 从 env 读取（ARB_AUTOTRADE_MIN_NET_SWAP_BPS，默认 0.2）。
// D-104 P0：swap facts 新鲜度门禁 fail-closed——now−f.Ts > SwapFactMaxAge → 跳过该 fact + 计数日志。
func (e *Engine) discoverSwapCombos(ctx context.Context) []swapComboCandidate {
	longFacts, err := e.store.LatestFacts(ctx, fact.KindSwapLong, "", "")
	if err != nil || len(longFacts) == 0 {
		return nil
	}
	shortFacts, err := e.store.LatestFacts(ctx, fact.KindSwapShort, "", "")
	if err != nil || len(shortFacts) == 0 {
		return nil
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
	// D-144 ①：同类型配对安全红线——两腿 is_real 必须相等（杜绝实盘×demo 假对冲）。查询失败 → 空 map（fail-open）。
	realMap, _ := e.store.ListAccountIsReal(ctx)
	var combos []swapComboCandidate
	minNetSwapBps := e.cfg.MinNetSwapBps
	// D-077 修复⑪B N5：删除 ==0 → 0.2 二次默认（config 已有 env 默认 0.2）。
	// 显式 0 = 全扫语义恢复（用户可通过 ARB_AUTOTRADE_MIN_NET_SWAP_BPS=0 关闭门槛）。
	for _, lf := range longFacts {
		// D-126 P1：删除 lf.Value<=0 单腿正 swap 过滤——套利看净 swap（long+short），
		// 非单腿。指数/商品/加密 CFD 的 swap_long 通常为负（broker 收过夜费），
		// 旧过滤挡住了所有非 FX 品种。净 swap 门槛由下方 netSwap < minNetSwapBps 守卫。
		for sfKey, sfEntry := range shortMap {
			if sfKey.canonical != lf.Symbol {
				continue
			}
			if sfKey.venue == lf.Venue {
				continue
			}
			// D-144 ①：同类型配对——实盘×实盘、demo×demo；跨类型跳过（假对冲事故语义）。
			if !isRealPair(realMap, lf.Venue, sfKey.venue) {
				continue
			}
			netSwap := lf.Value + sfEntry.value
			if netSwap < minNetSwapBps {
				continue
			}
			// D-077 修复⑪B N2：用 fact.Src 拼 venueKey（mt4 账户也能进 swap 套利）
			vLong := lf.Venue + ":" + lf.Src
			vShort := sfKey.venue + ":" + sfEntry.src
			// D-140 S3：基差 = (卖腿 bid − 买腿 ask) / 买腿 ask × 1e4（只展示不动排序）
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
			})
		}
	}
	sort.Slice(combos, func(i, j int) bool {
		return combos[i].netSwapBps > combos[j].netSwapBps
	})
	return combos
}
