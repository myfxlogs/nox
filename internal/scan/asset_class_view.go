// asset_class_view.go — D-043：asset_equiv 等价类视图（universe 跨 canonical join 与
// paper 组合键解析的单源）。MT canonical（broker_symbol_map.canonical 原形=swap facts 键）
// 与 CEX funding symbol（facts 键=cexFactSymbol 形）各经 asset_equiv 查 asset_class，
// 同类即 join；fin_symbol_aliases 退化为无覆盖兜底（XCU→COPPER 等）。
// 全部方法 nil 接收器安全——equiv 读失败时调用方传 nil 退化 legacy 口径。
package scan

import (
	"sort"
	"strings"

	"arb/internal/store/pgstore"
)

// assetClassView asset_equiv enabled 行的运行时投影（每步一次构建，禁逐行查库）。
type assetClassView struct {
	cexClass   map[string]string   // venue|perpCanonical → asset_class
	mtClass    map[string]string   // venue|mtCanonical → asset_class
	clsOfCanon map[string]string   // canonical → class（无 venue 兜底：hedge_canonical 等）
	cexByCls   map[string][]string // class|venue → perp canonicals（排序定，确定序）
	mtByCls    map[string]string   // class|venue → mt canonical（首行）
}

func newAssetClassView(rows []pgstore.AssetEquivEntry) *assetClassView {
	v := &assetClassView{
		cexClass:   map[string]string{},
		mtClass:    map[string]string{},
		clsOfCanon: map[string]string{},
		cexByCls:   map[string][]string{},
		mtByCls:    map[string]string{},
	}
	for _, e := range rows {
		if _, dup := v.clsOfCanon[e.Canonical]; !dup {
			v.clsOfCanon[e.Canonical] = e.AssetClass
		}
		if e.VenueType == "perp" {
			v.cexClass[e.Venue+"|"+e.Canonical] = e.AssetClass
			v.cexByCls[e.AssetClass+"|"+e.Venue] = append(v.cexByCls[e.AssetClass+"|"+e.Venue], e.Canonical)
		} else if strings.HasPrefix(e.VenueType, "mt") {
			v.mtClass[e.Venue+"|"+e.Canonical] = e.AssetClass
			if _, dup := v.mtByCls[e.AssetClass+"|"+e.Venue]; !dup {
				v.mtByCls[e.AssetClass+"|"+e.Venue] = e.Canonical
			}
		}
	}
	for _, cs := range v.cexByCls {
		sort.Strings(cs)
	}
	return v
}

func (v *assetClassView) cexClassOf(venue, factsSym string) string {
	if v == nil {
		return ""
	}
	return v.cexClass[venue+"|"+factsSym]
}

func (v *assetClassView) mtClassOf(venue, canonical string) string {
	if v == nil {
		return ""
	}
	return v.mtClass[venue+"|"+canonical]
}

// classOfCanon 无 venue 键形查类（fin_cex_universe.hedge_canonical 等）。
func (v *assetClassView) classOfCanon(canonical string) string {
	if v == nil {
		return ""
	}
	return v.clsOfCanon[canonical]
}

// cexCanons class×venue 的 perp canonical 列（确定排序；空=该 venue 无注册腿）。
func (v *assetClassView) cexCanons(class, venue string) []string {
	if v == nil {
		return nil
	}
	return v.cexByCls[class+"|"+venue]
}

// mtCanon class×venue 的 MT canonical（asset_equiv 原形；空=无注册腿）。
func (v *assetClassView) mtCanon(class, venue string) string {
	if v == nil {
		return ""
	}
	return v.mtByCls[class+"|"+venue]
}

// joinKeys 单腿 join 键集=legacy canonical ∪ asset_class（双侧同类即相遇；
// 单侧无覆盖时 legacy 键仍兜底互通——新 venue 未注册时 GOLD≠XAU 静默漏join 防）。
func (v *assetClassView) joinKeys(legacyCanonical, cls string) []string {
	if cls == "" || cls == legacyCanonical {
		return []string{legacyCanonical}
	}
	return []string{legacyCanonical, cls}
}

// cfdPairCandidates monitor pair 键候选（D-043 ② 组合键同步）：symbol=class 时经
// asset_equiv 实解析 venue 双腿 canonical（"XAGUSDT-SILVER" 实形），legacy 同 canonical
// 行仍走原公式。返回去重候选列，legacy 式先行——调用方按序 first-hit。
func (v *assetClassView) cfdPairCandidates(symbol, cexVenue, mtVenue string) []string {
	out := []string{paperCfdPairKey(symbol)}
	if v == nil {
		return out
	}
	if mt := v.mtCanon(symbol, mtVenue); mt != "" {
		for _, cc := range v.cexCanons(symbol, cexVenue) {
			p := cc + "-" + strings.TrimSuffix(mt, "USD")
			if p != out[0] {
				out = append(out, p)
			}
		}
	}
	return out
}

// CfdBookSpec D-144：簿对展开的写侧投影——每候选带 exSym（该 MT 账户 ticks 实查键）
// 与 CexCanon（DB 键形 perp canonical，调用方按 venue 约定转原生符号）。pair 键集与
// cfdPairCandidates 逐行同序（读侧候选=写侧簿行，单源防公式漂移）。
type CfdBookSpec struct{ Pair, ExSym, CexCanon string }

func (v *assetClassView) cfdBookSpecs(canonical, cexVenue, mtVenue string) []CfdBookSpec {
	mt := v.mtCanon(canonical, mtVenue)
	if mt == "" {
		mt = canonical // 无 equiv 行退 legacy 口径（与 cfdPairCandidates 首候选同域）
	}
	out := []CfdBookSpec{{Pair: paperCfdPairKey(canonical), ExSym: mt, CexCanon: canonical + "USDT"}}
	if v.mtCanon(canonical, mtVenue) != "" {
		for _, cc := range v.cexCanons(canonical, cexVenue) {
			p := cc + "-" + strings.TrimSuffix(mt, "USD")
			if p != out[0].Pair {
				out = append(out, CfdBookSpec{Pair: p, ExSym: mt, CexCanon: cc})
			}
		}
	}
	return out
}

// CfdBookSpecs 跨包出口：xasset-monitor 簿派生与开仓覆盖闸共用（equiv=ListAssetEquiv 行集）。
func CfdBookSpecs(equiv []pgstore.AssetEquivEntry, canonical, cexVenue, mtVenue string) []CfdBookSpec {
	return newAssetClassView(equiv).cfdBookSpecs(canonical, cexVenue, mtVenue)
}
