// graduate_resolve.go — D-043：毕业 canonical 双解析（自 graduate.go 拆出，300 行门禁）。
// SymbolMapEntry/ResolvedCfdPair/ResolveGraduationCanonicals 纯搬移+D-043 class 键扩展。
package scan

import (
	"fmt"

	"nox/internal/data/keydomain"
	"nox/internal/store/pgstore"
)

// SymbolMapEntry broker_symbol_map 行的解析投影（调用方自 store.ListSymbolMappings 转换，
// scan 不反向依赖 dashboard 装配层类型）。
type SymbolMapEntry struct {
	Canonical string
	IsPrimary bool
}

// ResolvedCfdPair canonical 双解析产物（S3 approve 写组合/S4 detector hint 构造共用）。
type ResolvedCfdPair struct {
	MtCanonical  string // broker_symbol_map canonical（detector 形，如 XAUUSD）
	MtVenueKey   string // "{mt_venue}:{mt4|mt5}"
	CexCanonical string // symbol+"USDT"（XAU→XAUUSDT/BTC→BTCUSDT/US30→US30USDT 实证规则）
}

// ResolveGraduationCanonicals D-403 S3/S4：canonical 双解析**单一 resolver**（KB
// cex-canonical-fallback-from-symbol：canonical 三套命名空间并存是全案最脆缝，禁两处各写）。
// mt 侧 broker_symbol_map 查 NormalizeJoinKey(canonical)==symbol（is_primary 优先否则任一行）；
// 映射缺失=error fail-closed 不臆造。cex venue key 由调用方注入（approve 取 paper venue 组合串、
// hint 取 universe 行 cex_venue——resolver 不经手 venue 归属，只钉 canonical 命名与 join 规范化单源）。
// symbol=asset_class 键行（SILVER×XAGUSDT 等）经 equiv 双解析——mt 腿 class→canonical
// 再走原 NJK 匹配；cex 腿 class|cexVenue→perp canonical（唯一才取，多注册=歧义 fail-closed，
// 该 venue 零注册腿=fail-closed 不回退 symbol+USDT 臆造）。
func ResolveGraduationCanonicals(symbol, mtVenue, cexVenue string, platform int,
	mappings []SymbolMapEntry, equiv []pgstore.AssetEquivEntry) (ResolvedCfdPair, error) {
	if symbol == "" || mtVenue == "" {
		return ResolvedCfdPair{}, fmt.Errorf("graduation canonical: empty symbol/mtVenue")
	}
	matchMt := func(njk string) string {
		mtCanon, primary := "", ""
		for _, m := range mappings {
			if NormalizeJoinKey(m.Canonical) == njk {
				if primary == "" {
					primary = m.Canonical
				}
				if m.IsPrimary {
					mtCanon = m.Canonical
					break
				}
			}
		}
		if mtCanon == "" {
			mtCanon = primary
		}
		return mtCanon
	}
	view := newAssetClassView(equiv)
	mtCanon := matchMt(symbol)
	if mtCanon == "" {
		if mc := view.mtCanon(symbol, mtVenue); mc != "" { // D-043：class 键行→等价类解析
			mtCanon = matchMt(NormalizeJoinKey(mc))
		}
	}
	if mtCanon == "" {
		return ResolvedCfdPair{}, fmt.Errorf("graduation canonical: no broker_symbol_map row venue=%s symbol=%s（fail-closed 不臆造）", mtVenue, symbol)
	}
	cexCanon := symbol + "USDT"
	switch cs := view.cexCanons(symbol, cexVenue); {
	case len(cs) == 1:
		cexCanon = cs[0]
	case len(cs) > 1:
		return ResolvedCfdPair{}, fmt.Errorf("graduation canonical: class %s venue %s 多 perp canonical %v（歧义 fail-closed）", symbol, cexVenue, cs)
	case view.classOfCanon(symbol) != "" || view.mtCanon(symbol, mtVenue) != "":
		return ResolvedCfdPair{}, fmt.Errorf("graduation canonical: class %s venue %s 无注册 perp 腿（fail-closed）", symbol, cexVenue)
	}
	if platform != 1 {
		return ResolvedCfdPair{}, fmt.Errorf("graduation canonical: venue %s platform=%d——MT4 已退役（fail-closed）", mtVenue, platform)
	}
	pt := "mt5"
	return ResolvedCfdPair{
		MtCanonical:  mtCanon,
		MtVenueKey:   keydomain.Join(mtVenue, pt),
		CexCanonical: cexCanon,
	}, nil
}
