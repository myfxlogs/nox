// service_graduation_risk_legs.go：D-476a 行数治理拆分——自 service_graduation_risk.go
// 移出腿解析段（gradRiskLegs 结构→双腿解析/cexVenueKeyOf/resolveLegSymbol），
// 行为逐行不变；主文件保留档案组装与 D-476a 证据分级落点。
package dashboard

import (
	"arb/internal/keydomain"
	"context"
	"fmt"

	"arb/internal/scan"
	"arb/internal/store/pgstore"
)

func gradRiskLegs(g *pgstore.CarryGraduation, p *pgstore.PaperSpotCarry, mtPair *scan.ResolvedCfdPair) ([]riskLegSpec, error) {
	switch g.Structure {
	case "long_cfd_short_perp": // buy=MT sell=CEX
		return []riskLegSpec{
			{mtPair.MtVenueKey, mtPair.MtCanonical, "buy"},
			{cexVenueKeyOf(g.VenueB), mtPair.CexCanonical, "sell"},
		}, nil
	case "short_cfd_long_perp": // buy=CEX sell=MT
		return []riskLegSpec{
			{cexVenueKeyOf(g.VenueB), mtPair.CexCanonical, "buy"},
			{mtPair.MtVenueKey, mtPair.MtCanonical, "sell"},
		}, nil
	case "spot_carry": // 双腿同所：perp 腿+spot 腿；Side 定 funding 方向
		v := g.VenueA
		perpRole, spotRole := "sell", "buy"
		if p != nil && p.Side == "long_perp" {
			perpRole, spotRole = "buy", "sell"
		}
		return []riskLegSpec{
			{keydomain.Join(v, "perp"), g.Symbol, perpRole},
			{keydomain.Join(v, "spot"), g.Symbol, spotRole},
		}, nil
	case "long_spot_short_perp": // D-555 R1a：双CEX——腿键走 cexCexLegKeys 行数据合成（与 approve 同径）
		if p == nil {
			return nil, fmt.Errorf("long_spot_short_perp paper 行缺失")
		}
		vb, vs, estr := cexCexLegKeys(g, p)
		if estr != "" {
			return nil, fmt.Errorf("%s", estr)
		}
		return []riskLegSpec{
			{vb, g.Symbol, "buy"},
			{vs, g.Symbol, "sell"},
		}, nil
	case "perp_perp": // venue=shortV×longV（方向在键，radar 轨）
		if g.VenueA == "" || g.VenueB == "" {
			return nil, fmt.Errorf("perp_perp venue 配对臂缺失 %q", g.VenuePair())
		}
		return []riskLegSpec{
			{keydomain.Join(g.VenueB, "perp"), g.Symbol, "buy"},
			{keydomain.Join(g.VenueA, "perp"), g.Symbol, "sell"},
		}, nil
	case "cfd_cfd": // mt×mt：MtVenue=short 腿（paper_mtm 插入约定），另一腿=buy
		if g.VenueA == "" || g.VenueB == "" || g.MtVenue == nil {
			return nil, fmt.Errorf("cfd_cfd venue/mt_venue 异常 %q", g.VenuePair())
		}
		short, long := *g.MtVenue, g.VenueA
		if short == g.VenueA {
			long = g.VenueB
		}
		return []riskLegSpec{
			{keydomain.Join(long, "mt5"), g.Symbol, "buy"},
			{keydomain.Join(short, "mt5"), g.Symbol, "sell"},
		}, nil
	}
	return nil, fmt.Errorf("未知 structure %q", g.Structure)
}

// cexVenueKeyOf CEX 臂裸名→cex:perp 键（与 approve 同口径；D-498 直读 venue_b 不再拆串）。
func cexVenueKeyOf(cexVenue string) string {
	return keydomain.Join(cexVenue, "perp")
}

// resolveLegSymbol 腿 spec 的 venue 原生 symbol 解析：
// MT 腿=broker_symbol_map（canonical→primary 行）；CEX 腿=fin_cex_universe（hedge_canonical→symbol）；
// spot 腿/universe 无映射→canonical 本身（tick 同键多数同形，miss 走 gaps 如实标）。
func (s *Service) resolveLegSymbol(ctx context.Context, leg *riskLegSpec, gaps *[]string) {
	vk := leg.venueKey
	k := keydomain.Parse(vk)
	bare, vt := k.Venue, k.Type
	switch vt {
	case "mt4", "mt5":
		mappings, err := s.gradDB().ListSymbolMappings(ctx, bare)
		if err != nil {
			*gaps = append(*gaps, vk+" broker_symbol_map 读取失败: "+err.Error())
			return
		}
		var pick *pgstore.SymbolMapping
		for i := range mappings {
			m := &mappings[i]
			if m.Canonical == leg.symbol && (pick == nil || m.IsPrimary) {
				pick = m
			}
		}
		if pick == nil {
			*gaps = append(*gaps, vk+" canonical "+leg.symbol+" 无映射行")
			return
		}
		leg.symbol = pick.BrokerSymbol
	case "perp":
		uni, err := s.gradDB().ListCexUniverse(ctx)
		if err != nil {
			*gaps = append(*gaps, vk+" universe 读取失败: "+err.Error())
			return
		}
		want := scan.NormalizeJoinKey(leg.symbol)
		for _, r := range uni {
			if r.Venue == bare && (r.HedgeCanonical == leg.symbol || scan.NormalizeJoinKey(r.HedgeCanonical) == want) {
				leg.symbol = r.Symbol
				return
			}
		}
		// universe 无 hedge 映射→symbol=canonical 直用（tick 键同形，缺则 gaps 标）。
	}
}

// graduationRiskReport S1 档案主函数。notional 优先序=combo spec>paper.QtyUSD（B-010）。
