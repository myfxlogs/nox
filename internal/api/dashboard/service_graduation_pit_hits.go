// service_graduation_pit_hits.go —— D-587④ 坑签名命中透出助手（棘轮拆分）。
package dashboard

import (
	"nox/internal/econkb"
	"nox/internal/store/pgstore"
)

// pitHitLabels 坑签名命中标注（只读呈现不拦闸；库缺/匹配败=空集非阻塞）。
// 命中=「⚠命中坑 <id>（EA-xx）」串。各透出面馈 econkb.Input 子集：
// 毕业卡=canonical+venue 对；提案行=payoff_class+单 venue（提案无 canonical 列——
// 空键不约束，payoff_class 键签名照常命中）。
func pitHitLabels(in econkb.Input) []string {
	hits := econkb.MatchHits(econkb.Dir(), in)
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

// pitHitsFor D-587④ 毕业卡面馈入（canonical+venue 对；未馈 payoff_class）。
func pitHitsFor(g *pgstore.CarryGraduation) []string {
	return pitHitLabels(econkb.Input{
		Canonical: g.Symbol, VenueA: g.VenueA, VenueB: g.VenueB,
	})
}
