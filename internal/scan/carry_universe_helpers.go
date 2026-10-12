// carry_universe_helpers.go D-412：join/规范化/可交易性/换算 helpers（自主文件拆出，
// check-lines 软警告治理——F1-F4 后主文件回 300 内）。纯函数同包零行为差。
package scan

import (
	"strings"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
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

// cexFactSymbol universe raw symbol → funding facts 键（与各 venue collector 发射键同构：
// gate BTC_USDT→BTCUSDT、okx XAU-USDT-SWAP→XAUUSDT、binance BTCUSDT 原样即 canonical）。
// D-550：raw 键直查 facts 零命中→边界 fail-closed→accrued 恒 0 的根修。
func cexFactSymbol(raw string) string {
	s := strings.ReplaceAll(raw, "_", "")
	s = strings.ReplaceAll(s, "-", "")
	return strings.TrimSuffix(s, "SWAP")
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

// cexRawCanonIndex cex 腿 canonical→facts symbol 索引（D-474a S2：funding 边界事实键=
// venue+collector canonical 形=cexFactSymbol(Symbol)，gate XAU_USDT→XAUUSDT、
// okx XAU-USDT-SWAP→XAUUSDT；HedgeCanonical/Symbol 双路归一键）。D-550：原样回 raw 键
// （US30_USDT）直查 facts 零命中→边界 fail-closed。D-033：自 accrue 内联块提取，
// 自盯行评估共用同键（禁两处各写归一规则）。
func cexRawCanonIndex(uniRows map[string]pgstore.CexUniverseRow, view *assetClassView) map[string]string {
	idx := map[string]string{}
	clsIdx := map[string]string{}
	clsBad := map[string]bool{}
	for _, ur := range uniRows {
		// D-028 ②：键补 NJK(cexFactSymbol(symbol))——okx TRX-USDT-SWAP / gate TRX_USDT
		// 经 NJK 不归一（尾段非 USDT/USD），查键 canonical=TRX 永不命中 → fail-closed。
		// cexFactSymbol 先归 facts 键形（TRXUSDT）再 NJK（→TRX）与查询键对齐。
		for _, canon := range []string{ur.HedgeCanonical, ur.Symbol, NormalizeJoinKey(cexFactSymbol(ur.Symbol))} {
			if k := ur.Venue + "|" + NormalizeJoinKey(canon); canon != "" {
				idx[k] = cexFactSymbol(ur.Symbol)
			}
		}
		// D-043 ②：键补 venue|asset_class——universe 行 Canonical=class（SILVER×XAGUSDT 等）
		// 时 p.Symbol=class，NJK(class) 无映射线索 → class 键命中本行 facts 键。
		for _, cls := range []string{view.cexClassOf(ur.Venue, cexFactSymbol(ur.Symbol)), view.classOfCanon(ur.HedgeCanonical)} {
			if cls == "" {
				continue
			}
			k, v := ur.Venue+"|"+cls, cexFactSymbol(ur.Symbol)
			if old, ok := clsIdx[k]; ok && old != v {
				// 同类同 venue 多 perp canonical（GOLD=PAXG/XAUT/XAU）→ 键不可唯一
				// 绑定 facts 腿 → 删键诚实 miss（fail-closed 不错绑异腿 funding）。
				delete(clsIdx, k)
				clsBad[k] = true
				continue
			}
			if !clsBad[k] {
				clsIdx[k] = v
			}
		}
	}
	for k, v := range clsIdx {
		idx[k] = v
	}
	return idx
}

// mtRawIndex D-028 ⑥：MT broker_symbol 选择（is_primary 语义，pgstore.ResolvePrimarySymbol
// 同裁定内载版——列表一次拉取+双形 NJK 键天然兼容，零逐 canonical 查询开销）：
// primary 行优先 / 组内唯一行兜底（false+恰一行=隐式 primary，brokers_symbolmap 同注）/
// 多行无 primary=ambiguous 不设键（fail-closed，禁 last-write-wins——3001157 XAU 双行
// XAUUSDm/XAUUSD247m 旧径随机取，swap/tick facts 键漂移）。
func mtRawIndex(maps []pgstore.SymbolMapping) map[string]string {
	m := map[string]string{}
	seen := map[string]int{}
	for _, mm := range maps {
		if mm.Canonical == "" {
			continue
		}
		k := NormalizeJoinKey(mm.Canonical)
		seen[k]++
		if mm.IsPrimary {
			m[k] = mm.BrokerSymbol
		}
	}
	for _, mm := range maps {
		if mm.Canonical == "" {
			continue
		}
		k := NormalizeJoinKey(mm.Canonical)
		if _, done := m[k]; !done && seen[k] == 1 {
			m[k] = mm.BrokerSymbol
		}
	}
	return m
}
