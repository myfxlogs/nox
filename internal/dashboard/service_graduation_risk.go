// service_graduation_risk.go D-471 S1：毕业建议风险档案（盲批修复——approve 前业主
// 可见名义/逐腿量/资金需求/杠杆/强平距离/最坏场景）。纯后端计算，前端只渲染。
// 缺数据语义：任一解析失败→字段 NULL+gaps 注记（不静默 0）。 ticks 闸=5min（B-012）。
// S2 入场可行性复检：entryWindowOf 每行即时算（open/closed:<reason>），approve 侧
// 硬前置=GraduationQualifiedReason 现算复核（分叉前全结构双盖）。
package dashboard

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/fact"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

// riskTickMaxAge dossier 价格源 staleness 闸（B-012：与 paperCfdMonitorMaxAge 同口径 5min）。
const riskTickMaxAge = 5 * time.Minute

// riskBasisP95Window D-544：ticks 簿 adverse basis p95 尾部窗（≈monitor 簿实深 +
// 约束扫描成本——ticks 按小时分区，7d 成本可控）。
const riskBasisP95Window = 7 * 24 * time.Hour

// riskLegSpec 腿级输入（venue_key + venue 原生 symbol + 方向）。
type riskLegSpec struct {
	venueKey, symbol, role string // role=buy|sell
}

// gradRiskLegs 结构→双腿解析（B-011：按 :mt 后缀分 MT/CEX 腿，不假设「一 MT 一 CEX」；
// venue 组合串方向：cfd=mt×cex 由 structure 定向；perp_perp=shortV×longV；cfd_cfd=MtVenue=short 腿）。
// previewNotional 供 PreviewXAssetComboRisk 直接给定名义（>0 已校验）。
func (s *Service) graduationRiskReport(ctx context.Context, legs []riskLegSpec, notional float64, notionalSrc string, paper *pgstore.PaperSpotCarry) *v1.GraduationRiskReport {
	rep := &v1.GraduationRiskReport{NotionalUsd: &notional, NotionalSource: notionalSrc}
	// D-476a：字段证据级别在各自计算同点产出（A实结/B实数据/C估算/D缺）。
	eg := func(f string, g v1.EvidenceGrade) {
		rep.FieldGrades = append(rep.FieldGrades, &v1.FieldEvidence{Field: f, Grade: g})
	}
	var gaps []string
	var capital float64
	var perpLevs []float64 // D-565 S2：perp 腿 venue 实读杠杆收集（recommended 取 min 保守）
	hasPerpLeg := false
	capitalOK := true
	mtEstimated := false // D-006 S1b：MT 腿走悲观档估算（未实测）→ capital 降 C 级
	now := time.Now()
	for i := range legs {
		leg := &legs[i]
		s.resolveLegSymbol(ctx, leg, &gaps)
		out := &v1.GraduationRiskLeg{VenueKey: leg.venueKey, Symbol: leg.symbol, Role: leg.role}
		vk := leg.venueKey
		k := keydomain.Parse(vk)
		bare, vt := k.Venue, k.Type
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
				imr, err := s.gradDB().LatestFacts(ctx, fact.KindInitMarginRate, bare, "")
				rate, measured := effMTMarginRate(imr, err, time.Now())
				if !measured { // D-006 S1b：缺/陈按悲观档 20x（5%）估，注记不谎报实测
					gaps = append(gaps, vk+" init_margin_rate 未实测（按悲观档 20x 估）")
					mtEstimated = true
				}
				m := notional * rate
				out.MarginUsd = &m
			case "perp":
				hasPerpLeg = true
				// D-565 S2：venue 实读杠杆（gate=get_leverage/binance=v2-account/
				// okx=leverage-info）；读不到→标「实况不可得」不给数（恒 5x 硬编裁违宪）。
				if lev, lerr := s.venueLeverage(ctx, vk, leg.symbol, leg.role == "sell"); lerr == nil && lev > 0 {
					m := notional / lev
					out.MarginUsd = &m
					out.Note = fmt.Sprintf("margin 按 venue 实况杠杆 %.0fx", lev)
					perpLevs = append(perpLevs, lev)
				} else {
					gaps = append(gaps, vk+" 杠杆实况不可得（margin 不估）")
					capitalOK = false
				}
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
	eg("notional_usd", v1.EvidenceGrade_EVIDENCE_GRADE_B)
	eg("legs", v1.EvidenceGrade_EVIDENCE_GRADE_B)
	// D-565 S2：recommended_leverage=perp 腿 venue 实读杠杆取 min（保守）；
	// 有 perp 腿但全读败 → 0+「实况不可得」注记（不编数）；无 perp 腿→0 注记口径。
	switch {
	case len(perpLevs) > 0:
		rep.RecommendedLeverage = perpLevs[0]
		for _, lv := range perpLevs[1:] {
			if lv < rep.RecommendedLeverage {
				rep.RecommendedLeverage = lv
			}
		}
		rep.LeverageNote = "venue 实读杠杆（perp 腿取 min）"
	case hasPerpLeg:
		rep.LeverageNote = "venue 杠杆实况不可得"
	default:
		rep.LeverageNote = "无 perp 保证金腿"
	}
	// 实读得→B；读不到/无腿→C（注记形态保留）。
	levG := v1.EvidenceGrade_EVIDENCE_GRADE_B
	if len(perpLevs) == 0 {
		levG = v1.EvidenceGrade_EVIDENCE_GRADE_C
	}
	eg("recommended_leverage", levG)
	// 强平距离估算：mmr_est 缺取 1% 标估算（SSOT §3：禁伪造精确值）；账户实采 buffer_multiple→B。
	liqG := v1.EvidenceGrade_EVIDENCE_GRADE_C
	if snaps, err := s.liveAccountLatest(ctx); err == nil {
		bufSet := make(map[string]bool, len(snaps))
		for _, sn := range snaps {
			if sn.BufferMultiple != nil {
				bufSet[keydomain.Join(sn.Venue, sn.VenueType)] = true
			}
		}
		for _, leg := range legs {
			if bufSet[leg.venueKey] {
				liqG = v1.EvidenceGrade_EVIDENCE_GRADE_B
				break
			}
		}
	}
	// D-565：强平距离锚=venue 实读杠杆（不可得→NULL 不估，禁编 5x 锚）。
	if rep.RecommendedLeverage > 0 {
		liq := (1.0/rep.RecommendedLeverage - 0.01) * 100
		rep.LiqDistancePctEst = &liq
	} else {
		gaps = append(gaps, "杠杆实况不可得（强平距离不估）")
	}
	eg("liq_distance_pct_est", liqG)
	if capitalOK && capital > 0 {
		rep.CapitalUsd = &capital
		if mtEstimated { // D-006 S1b：含悲观档估算腿→C（估），实测才 B
			eg("capital_usd", v1.EvidenceGrade_EVIDENCE_GRADE_C)
		} else {
			eg("capital_usd", v1.EvidenceGrade_EVIDENCE_GRADE_B)
		}
	} else {
		eg("capital_usd", v1.EvidenceGrade_EVIDENCE_GRADE_D)
	}
	// 最坏场景：funding 翻号单日成本=notional×|worst_ann|/365（窗口 worst 缺→last_funding_ann）。
	worstAnn := 0.0
	for _, leg := range legs {
		if !keydomain.IsType(leg.venueKey, "perp") {
			continue
		}
		bare := keydomain.BareOf(leg.venueKey)
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
		eg("funding_flip_daily_usd", v1.EvidenceGrade_EVIDENCE_GRADE_C) // worst_ann 假设源恒 C
	} else {
		gaps = append(gaps, "funding 窗口无样本（翻号成本未知）")
		eg("funding_flip_daily_usd", v1.EvidenceGrade_EVIDENCE_GRADE_D)
	}
	// adverse_basis_p95：monitor 簿 venue 对 p95 折 usd（无簿→NULL+gap）。
	var cexV, mtV string
	for _, leg := range legs {
		if keydomain.IsType(leg.venueKey, "perp") {
			cexV = leg.venueKey
		}
		if keydomain.IsType(leg.venueKey, "mt5") {
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
			eg("adverse_basis_p95_usd", v1.EvidenceGrade_EVIDENCE_GRADE_B)
		} else {
			gaps = append(gaps, "monitor 簿无 "+cexV+"×"+mtV+" 样本（p95 缺）")
			eg("adverse_basis_p95_usd", v1.EvidenceGrade_EVIDENCE_GRADE_D)
		}
	} else {
		// D-544：非 mt×cex 结构改走 ticks 簿实算锚（monitor 簿 schema 硬绑 mt×cex，
		// 而 ticks venue_type∈{mt5,perp,spot} 三型全流=等效簿）。
		var buyLeg, sellLeg *riskLegSpec
		for i := range legs {
			switch legs[i].role {
			case "buy":
				buyLeg = &legs[i]
			case "sell":
				sellLeg = &legs[i]
			}
		}
		if buyLeg == nil || sellLeg == nil {
			gaps = append(gaps, "非 mt×cex 结构无 monitor 簿锚（p95 缺）")
			eg("adverse_basis_p95_usd", v1.EvidenceGrade_EVIDENCE_GRADE_D)
		} else if p95, ok, err := s.gradDB().TickAdverseBasisP95(ctx,
			buyLeg.venueKey, buyLeg.symbol, sellLeg.venueKey, sellLeg.symbol,
			now.Add(-riskBasisP95Window)); err == nil && ok {
			// 语义差注记（与 monitor 簿值口径不同=设计内）：monitor p95=离散采样点
			// 入场价差；tick 路径=连续 adverse 成本序列（含双腿 spread crossing）
			// →更保守，恰合 adverse anchor 用途；数据为实算仍 B 级。
			v := p95 * notional / 1e4
			if v < 0 {
				v = -v
			}
			rep.AdverseBasisP95Usd = &v
			eg("adverse_basis_p95_usd", v1.EvidenceGrade_EVIDENCE_GRADE_B)
		} else {
			gaps = append(gaps, fmt.Sprintf("ticks 簿无 %s×%s 对齐样本（p95 缺）", buyLeg.venueKey, sellLeg.venueKey))
			eg("adverse_basis_p95_usd", v1.EvidenceGrade_EVIDENCE_GRADE_D)
		}
	}
	// roundtrip_fee：paper 真费优先（D-474a 分腿真值）；缺→NULL 标缺（禁常数费）。
	if paper != nil && paper.EstRoundTripFeeUSD != nil {
		f := *paper.EstRoundTripFeeUSD
		rep.RoundtripFeeUsd = &f
		eg("roundtrip_fee_usd", v1.EvidenceGrade_EVIDENCE_GRADE_B)
	} else {
		gaps = append(gaps, "est_round_trip_fee 缺（往返费未知）")
		eg("roundtrip_fee_usd", v1.EvidenceGrade_EVIDENCE_GRADE_D)
	}
	for _, leg := range rep.Legs {
		rep.MinExecutable = append(rep.MinExecutable, leg.VenueKey+": "+leg.Note)
		mg := v1.EvidenceGrade_EVIDENCE_GRADE_B
		if strings.Contains(leg.Note, "lot filter 未采集") {
			mg = v1.EvidenceGrade_EVIDENCE_GRADE_D // 该腿 lot filter 未采
		}
		eg("min_executable:"+leg.VenueKey, mg)
	}
	rep.Gaps = gaps
	return rep
}
