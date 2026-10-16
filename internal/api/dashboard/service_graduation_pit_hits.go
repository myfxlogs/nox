// service_graduation_pit_hits.go —— D-587④ 坑签名命中透出助手（棘轮拆分）。
package dashboard

import (
	"nox/internal/econkb"
	"nox/internal/store/pgstore"
)

// pitHitsFor D-587④ 坑签名命中标注（毕业卡/批准单行透出——只读呈现不拦闸；
// 库缺/匹配败=空集非阻塞；命中=「⚠命中坑 <id>（EA-xx）」串）。
func pitHitsFor(g *pgstore.CarryGraduation) []string {
	hits := econkb.MatchHits(econkb.Dir(), econkb.Input{
		Canonical: g.Symbol, VenueA: g.VenueA, VenueB: g.VenueB,
	})
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		label := "⚠命中坑 " + h.ID
		if h.EALink != "" {
			label += "（" + h.EALink + "）"
		}
		out = append(out, label)
	}
	return out
}
