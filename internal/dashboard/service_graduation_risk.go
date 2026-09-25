// service_graduation_risk.go D-471 S1：毕业建议风险档案（盲批修复——approve 前业主
// 可见名义/逐腿量/资金需求/杠杆/强平距离/最坏场景）。纯后端计算，前端只渲染。
// 缺数据语义：任一解析失败→字段 NULL+gaps 注记（不静默 0）。 ticks 闸=5min（B-012）。
// S2 入场可行性复检：entryWindowOf 每行即时算（open/closed:<reason>），approve 侧
// 硬前置=GraduationQualifiedReason 现算复核（分叉前全结构双盖）。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/fact"
	"arb/internal/scan"
	"arb/internal/store/pgstore"
)

// riskTickMaxAge dossier 价格源 staleness 闸（B-012：与 paperCfdMonitorMaxAge 同口径 5min）。
const riskTickMaxAge = 5 * time.Minute

// riskLegSpec 腿级输入（venue_key + venue 原生 symbol + 方向）。
type riskLegSpec struct {
	venueKey, symbol, role string // role=buy|sell
}

// gradRiskLegs 结构→双腿解析（B-011：按 :mt 后缀分 MT/CEX 腿，不假设「一 MT 一 CEX」；
// venue 组合串方向：cfd=mt×cex 由 structure 定向；perp_perp=shortV×longV；cfd_cfd=MtVenue=short 腿）。
func gradRiskLegs(g *pgstore.CarryGraduation, p *pgstore.PaperSpotCarry, mtPair *scan.ResolvedCfdPair) ([]riskLegSpec, error) {
	switch g.Structure {
	case "long_cfd_short_perp": // buy=MT sell=CEX
		return []riskLegSpec{
			{mtPair.MtVenueKey, mtPair.MtCanonical, "buy"},
			{cexVenueKeyOf(g.Venue), mtPair.CexCanonical, "sell"},
		}, nil
	case "short_cfd_long_perp": // buy=CEX sell=MT
		return []riskLegSpec{
			{cexVenueKeyOf(g.Venue), mtPair.CexCanonical, "buy"},
			{mtPair.MtVenueKey, mtPair.MtCanonical, "sell"},
		}, nil
	case "spot_carry": // 双腿同所：perp 腿+spot 腿；Side 定 funding 方向
		v := strings.Split(g.Venue, "×")[0]
		perpRole, spotRole := "sell", "buy"
		if p != nil && p.Side == "long_perp" {
			perpRole, spotRole = "buy", "sell"
		}
		return []riskLegSpec{
			{v + ":perp", g.Symbol, perpRole},
			{v + ":spot", g.Symbol, spotRole},
		}, nil
	case "perp_perp": // venue=shortV×longV（方向在键，radar 轨）
		parts := strings.Split(g.Venue, "×")
		if len(parts) != 2 {
			return nil, fmt.Errorf("perp_perp venue 组合串异常 %q", g.Venue)
		}
		return []riskLegSpec{
			{parts[1] + ":perp", g.Symbol, "buy"},
			{parts[0] + ":perp", g.Symbol, "sell"},
		}, nil
	case "cfd_cfd": // mt×mt：MtVenue=short 腿（paper_mtm 插入约定），另一腿=buy
		parts := strings.Split(g.Venue, "×")
		if len(parts) != 2 || g.MtVenue == nil {
			return nil, fmt.Errorf("cfd_cfd venue/mt_venue 异常 %q", g.Venue)
		}
		short, long := *g.MtVenue, parts[0]
		if short == parts[0] {
			long = parts[1]
		}
		return []riskLegSpec{
			{long + ":mt5", g.Symbol, "buy"},
			{short + ":mt5", g.Symbol, "sell"},
		}, nil
	}
	return nil, fmt.Errorf("未知 structure %q", g.Structure)
}

// cexVenueKeyOf mt×cex 组合串尾段→cex:perp 键（与 approve 同口径）。
func cexVenueKeyOf(paperVenue string) string {
	parts := strings.Split(paperVenue, "×")
	return parts[len(parts)-1] + ":perp"
}

// resolveLegSymbol 腿 spec 的 venue 原生 symbol 解析：
// MT 腿=broker_symbol_map（canonical→primary 行）；CEX 腿=fin_cex_universe（hedge_canonical→symbol）；
// spot 腿/universe 无映射→canonical 本身（tick 同键多数同形，miss 走 gaps 如实标）。
func (s *Service) resolveLegSymbol(ctx context.Context, leg *riskLegSpec, gaps *[]string) {
	vk := leg.venueKey
	i := strings.LastIndex(vk, ":")
	bare, vt := vk[:i], vk[i+1:]
	switch vt {
	case "mt4", "mt5":
		mappings, err := s.gradDB().ListSymbolMappings(ctx, bare)
		if err != nil {
			*gaps = append(*gaps, vk+" symbol_map 读取失败: "+err.Error())
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
// previewNotional 供 PreviewXAssetComboRisk 直接给定名义（>0 已校验）。
func (s *Service) graduationRiskReport(ctx context.Context, legs []riskLegSpec, notional float64, notionalSrc string, paper *pgstore.PaperSpotCarry) *v1.GraduationRiskReport {
	rep := &v1.GraduationRiskReport{NotionalUsd: &notional, NotionalSource: notionalSrc}
	var gaps []string
	var capital float64
	capitalOK := true
	now := time.Now()
	for i := range legs {
		leg := &legs[i]
		s.resolveLegSymbol(ctx, leg, &gaps)
		out := &v1.GraduationRiskLeg{VenueKey: leg.venueKey, Symbol: leg.symbol, Role: leg.role}
		vk := leg.venueKey
		ci := strings.LastIndex(vk, ":")
		bare, vt := vk[:ci], vk[ci+1:]
		// 价格源：ticks 最新 mid，staleness ≤5min 超龄→NULL+gaps。
		var mid float64
		t, ok, err := s.gradDB().LatestTick(ctx, bare, vt, leg.symbol)
		if err != nil {
			gaps = append(gaps, vk+" tick 读取失败: "+err.Error())
		} else if !ok {
			gaps = append(gaps, vk+"/"+leg.symbol+" 无 tick")
		} else if now.Sub(t.Ts) > riskTickMaxAge {
			gaps = append(gaps, vk+"/"+leg.symbol+" 价格数据过期（"+now.Sub(t.Ts).Round(time.Second).String()+">5min）")
		} else {
			mid = (t.Bid + t.Ask) / 2
		}
		var ct float64 = 1
		if mid > 0 {
			qusd := notional // 每腿名义=notional（双腿对冲同额）
			out.QtyUsd = &qusd
			switch vt {
			case "mt4", "mt5":
				mappings, _ := s.gradDB().ListSymbolMappings(ctx, bare)
				for _, m := range mappings {
					if m.BrokerSymbol == leg.symbol && m.ContractSize != nil {
						ct = *m.ContractSize
					}
				}
				if ct <= 1 {
					gaps = append(gaps, vk+"/"+leg.symbol+" contract_size 未知（按 1 估）")
				}
			case "perp":
				uni, _ := s.gradDB().ListCexUniverse(ctx)
				if r, ok := uni[bare+"|"+leg.symbol]; ok && r.CtVal != nil {
					ct = *r.CtVal
				} else {
					gaps = append(gaps, vk+"/"+leg.symbol+" ct_val 未知（按 1 估）")
				}
			}
			qty := notional / (mid * ct)
			out.QtyNative = &qty
			// 腿资金需求：MT=notional×init_margin_rate；CEX perp=notional/lev_rec；spot=全款。
			switch vt {
			case "mt4", "mt5":
				imr, err := s.gradDB().LatestFacts(ctx, fact.KindInitMarginRate, bare, bare)
				if err == nil && len(imr) > 0 && imr[0].Value > 0 {
					m := notional * imr[0].Value
					out.MarginUsd = &m
				} else {
					gaps = append(gaps, vk+" init_margin_rate 缺（保证金未知）")
					capitalOK = false
				}
			case "perp":
				m := notional / 5 // recommended_leverage=5（venue 可得档未采集）
				out.MarginUsd = &m
				out.Note = "margin 按 5x 杠杆估（venue 可得档未采集）"
			default: // spot
				m := notional
				out.MarginUsd = &m
				out.Note = "现货腿=名义全款"
			}
			// min_executable：1 手/1 张折 usd（lot filter 未采集→如实标）
			out.Note = strings.TrimSpace(out.Note + fmt.Sprintf("；最小单位 1ct≈$%.2f（lot filter 未采集）", mid*ct))
			if out.MarginUsd != nil {
				capital += *out.MarginUsd
			}
		}
		rep.Legs = append(rep.Legs, out)
	}
	rep.RecommendedLeverage = 5
	rep.LeverageNote = "min(5, venue 可得档)；可得档未采集按 5 给"
	// 强平距离估算：mmr_est 缺取 1% 标估算（SSOT §3：禁伪造精确值）。
	liq := (1.0/5 - 0.01) * 100
	rep.LiqDistancePctEst = &liq
	if capitalOK && capital > 0 {
		rep.CapitalUsd = &capital
	}
	// 最坏场景：funding 翻号单日成本=notional×|worst_ann|/365（窗口 worst 缺→last_funding_ann）。
	worstAnn := 0.0
	for _, leg := range legs {
		if !strings.HasSuffix(leg.venueKey, ":perp") {
			continue
		}
		bare := strings.TrimSuffix(leg.venueKey, ":perp")
		rows, err := s.gradDB().FundingPredWindow(ctx, bare, leg.symbol, now.Add(-s.carryRegime.Window))
		if err != nil || len(rows) == 0 {
			continue
		}
		for _, r := range rows {
			if a := r.Value; a < 0 && -a > worstAnn {
				worstAnn = -a
			}
		}
	}
	if worstAnn == 0 && paper != nil && paper.LastFundingAnn != nil {
		worstAnn = *paper.LastFundingAnn
		if worstAnn < 0 {
			worstAnn = -worstAnn
		}
	}
	if worstAnn > 0 {
		f := notional * worstAnn / 100 / 365
		rep.FundingFlipDailyUsd = &f
	} else {
		gaps = append(gaps, "funding 窗口无样本（翻号成本未知）")
	}
	// adverse_basis_p95：monitor 簿 venue 对 p95 折 usd（无簿→NULL+gap）。
	var cexV, mtV string
	for _, leg := range legs {
		if strings.HasSuffix(leg.venueKey, ":perp") {
			cexV = leg.venueKey
		}
		if strings.HasSuffix(leg.venueKey, ":mt5") {
			mtV = leg.venueKey
		}
	}
	if cexV != "" && mtV != "" {
		if p95, ok, err := s.gradDB().XAssetMonitorSpreadP95(ctx, cexV, mtV); err == nil && ok {
			v := p95 * notional / 1e4
			if v < 0 {
				v = -v
			}
			rep.AdverseBasisP95Usd = &v
		} else {
			gaps = append(gaps, "monitor 簿无 "+cexV+"×"+mtV+" 样本（p95 缺）")
		}
	} else {
		gaps = append(gaps, "非 mt×cex 结构无 monitor 簿锚（p95 缺）")
	}
	// roundtrip_fee：paper 真费优先（D-474a 分腿真值）；缺→NULL 标缺（禁常数费）。
	if paper != nil && paper.EstRoundTripFeeUSD != nil {
		f := *paper.EstRoundTripFeeUSD
		rep.RoundtripFeeUsd = &f
	} else {
		gaps = append(gaps, "est_round_trip_fee 缺（往返费未知）")
	}
	for _, leg := range rep.Legs {
		rep.MinExecutable = append(rep.MinExecutable, leg.VenueKey+": "+leg.Note)
	}
	rep.Gaps = gaps
	return rep
}
