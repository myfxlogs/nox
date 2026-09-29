package dashboard

import (
	"fmt"

	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// cexCexLegKeys long_spot_short_perp（D-555 双CEX跨所轨）腿键合成——取行数据
// venue_a/b+venue_type_a/b（禁 canonical 反推 venue、禁 Join 硬钉类型）：
// 恰一 spot 臂=买腿、恰一 perp 臂=卖腿，任一不符/缺列 fail-closed。
func cexCexLegKeys(grad *pgstore.CarryGraduation, paper *pgstore.PaperSpotCarry) (buy, sell, errStr string) {
	if grad.Symbol == "" || grad.Symbol != paper.Symbol {
		return "", "", fmt.Sprintf("双CEX 建议 #%d canonical 构型不符（symbol=%q vs paper=%q），拒绝授权", grad.ID, grad.Symbol, paper.Symbol)
	}
	if grad.VenueTypeA != "spot" || grad.VenueTypeB != "perp" || grad.VenueA == "" || grad.VenueB == "" {
		return "", "", fmt.Sprintf("双CEX 建议 #%d 腿型不符（须 A=spot×B=perp，实际 %s:%s×%s:%s），拒绝授权",
			grad.ID, grad.VenueA, grad.VenueTypeA, grad.VenueB, grad.VenueTypeB)
	}
	return keydomain.Join(grad.VenueA, grad.VenueTypeA), keydomain.Join(grad.VenueB, grad.VenueTypeB), ""
}

// gradVenueDisplay 建议行 venue 展示串（D-555 S4）：venue_type 齐（双CEX轨）→
// 型化键 'a:tA×b:tB'（前端 venuePairCn 逐段 venueLabel →「gate·现货×binance·永续」）；
// 存量行 type 空 → 裸配对串不变（展示字段，语义键仍在 venue_a/b 源列）。
func gradVenueDisplay(g *pgstore.CarryGraduation) string {
	if g.VenueTypeA == "" || g.VenueTypeB == "" || g.VenueB == "" {
		return g.VenuePair()
	}
	return keydomain.Join(g.VenueA, g.VenueTypeA) + "×" + keydomain.Join(g.VenueB, g.VenueTypeB)
}
