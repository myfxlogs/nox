// carry_universe_helpers.go D-412：join/规范化/可交易性/换算 helpers（自主文件拆出，
// check-lines 软警告治理——F1-F4 后主文件回 300 内）。纯函数同包零行为差。
package scan

import (
	"strings"

	"arb/internal/fact"
)

// modeAllowed D-382 S1：方向感知可交易性（detector.legTradable 语义对齐、独立实现防包耦合）。
// 买腿（long_cfd）要求 mode∈{LongOnly,Full}；卖腿（short_cfd）∈{ShortOnly,Full}；
// CloseOnly/Disabled 皆拦开新仓——用「允许集」判定不是「≠0」。
func modeAllowed(mode int64, buy bool) bool {
	if buy {
		return mode == fact.TradeModeLongOnly || mode == fact.TradeModeFull
	}
	return mode == fact.TradeModeShortOnly || mode == fact.TradeModeFull
}

// cexCanonical CEX symbol→canonical（detector/采集器现行规则同构：去 USDT 后缀）。
// BTCUSDT→BTC；无法映射（无 USDT 后缀）→ ""。
func cexCanonical(symbol string) string {
	if strings.HasSuffix(symbol, "USDT") && len(symbol) > 4 {
		return strings.TrimSuffix(symbol, "USDT")
	}
	return ""
}

// NormalizeJoinKey D-336R1：join 键双侧规范化（D-403 起导出：毕业链 approve 解析与 detector hint 适配共用，禁两处各写）——canonical 双形分裂（MT map 侧
// XAUUSD vs CEX 侧 XAU）永不相等致黄金/BTC/PAXG 全 join 失败的层内根修
// （系统级两形并存=既有债不动）。去尾部 _、依次尝试去 USDT/USD 后缀
// （去后非空才去，USDT 优先）：XAUUSD→XAU、XAUUSDT→XAU、SPX500_→SPX500、BTCUSDT→BTC。
func NormalizeJoinKey(s string) string {
	s = strings.TrimSuffix(s, "_")
	if r := strings.TrimSuffix(s, "USDT"); r != "" && r != s {
		return r
	}
	if r := strings.TrimSuffix(s, "USD"); r != "" && r != s {
		return r
	}
	return s
}

// applyAlias D-464 S1：canonical 命中别名表→替换；否则原样（纯函数，map 一次载入传入）。
func applyAlias(canonical string, aliases map[string]string) string {
	if c, ok := aliases[canonical]; ok && c != "" {
		return c
	}
	return canonical
}

// cleanAliases D-464 S1：载入时检测循环（A→B→A 或链回环）剔除并返回剔除清单（warn 用）。
// 非循环链（A→B，B→C）允许逐级解析，深度上限=len(map)。
func cleanAliases(in map[string]string) (map[string]string, []string) {
	var dropped []string
	out := map[string]string{}
	for a, c := range in {
		seen := map[string]bool{a: true}
		cur, cyclic := c, false
		for {
			nxt, ok := in[cur]
			if !ok {
				break // cur=终 canonical（非 alias 键）
			}
			if seen[cur] {
				cyclic = true
				break
			}
			seen[cur] = true
			cur = nxt
		}
		if cyclic || cur == "" {
			dropped = append(dropped, a)
			continue
		}
		out[a] = cur // 预解析到终 canonical
	}
	return out, dropped
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}
