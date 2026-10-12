// service_graduation_entry.go D-471：档案入口（gradDossier）+入场窗口复检（entryWindowOf）
// ——自 service_graduation_risk.go 拆出（check-lines 行数治理 >300 软警告口径）。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"time"

	"nox/internal/data/keydomain"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/research/scan"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// gradDossier 建议行→档案：腿解析（cfd 需 canonical resolver）+notional 优先序。
func (s *Service) gradDossier(ctx context.Context, g *pgstore.CarryGraduation, p *pgstore.PaperSpotCarry) *v1.GraduationRiskReport {
	var mtPair *scan.ResolvedCfdPair
	var preErr error
	if isCfdStructure(g.Structure) {
		if g.MtVenue == nil {
			preErr = fmt.Errorf("mt_venue 缺失")
		} else if platform, err := s.mtPlatformFor(ctx, *g.MtVenue); err != nil {
			preErr = err
		} else {
			mappings, err := s.gradDB().ListSymbolMappings(ctx, *g.MtVenue)
			if err != nil {
				preErr = err
			} else {
				equiv, eqErr := s.gradDB().ListAssetEquiv(ctx)
				if eqErr != nil { // D-043：降级 nil=legacy 解析（class 行 resolver 自会 fail-closed）
					log.Printf("[graduation] asset_equiv read: %v", eqErr)
				}
				pair, err := scan.ResolveGraduationCanonicals(g.Symbol, *g.MtVenue, g.VenueB, platform, symbolMapEntries(mappings), equiv)
				if err != nil {
					preErr = err
				} else {
					mtPair = &pair
				}
			}
		}
	}
	notional := 0.0
	src := "paper"
	if p != nil {
		notional = p.QtyUSD
	}
	if notional <= 0 {
		r := &v1.GraduationRiskReport{NotionalSource: src}
		r.Gaps = []string{"notional<=0 拒绝出档（防盲签 0 名义）"}
		if preErr != nil {
			r.Gaps = append(r.Gaps, preErr.Error())
		}
		return r
	}
	if isCfdStructure(g.Structure) && mtPair == nil {
		r := &v1.GraduationRiskReport{NotionalUsd: &notional, NotionalSource: src}
		r.Gaps = []string{"canonical 解析失败: " + preErr.Error()}
		return r
	}
	legs, err := gradRiskLegs(g, p, mtPair)
	if err != nil {
		r := &v1.GraduationRiskReport{NotionalUsd: &notional, NotionalSource: src}
		r.Gaps = []string{err.Error()}
		if preErr != nil {
			r.Gaps = append(r.Gaps, preErr.Error())
		}
		return r
	}
	return s.graduationRiskReport(ctx, legs, notional, src, p)
}

// entryWindowOf S2 入场可行性复检（即时算）：cfd/perp_perp→CarryFundingRegimeCheck
// 同判据；spot_carry=venue 无 :perp 后缀禁直接复用（B-014 假闸）→perp 腿单跑
// FundingRegimeOK，requiredSign 按 paper.Side（short_perp=+1 收息/long_perp=−1）；
// long_spot_short_perp 同径——perp 臂在 VenueB（D-555 R1b）；
// cfd_cfd 无 funding 收益腿→恒 open（swap 域不在本闸覆盖）。
func (s *Service) entryWindowOf(ctx context.Context, g *pgstore.CarryGraduation, p *pgstore.PaperSpotCarry) string {
	cfg := s.carryRegime
	now := time.Now()
	switch g.Structure {
	case "spot_carry", "long_spot_short_perp":
		if p == nil {
			return "closed:paper 行缺"
		}
		sign := 1.0
		if p.Side == "long_perp" {
			sign = -1
		}
		// D-555 R1b：双CEX 轨 perp 臂在 VenueB（spot_carry 同所双腿=VenueA）。
		bare := g.VenueA
		if g.Structure == "long_spot_short_perp" {
			bare = g.VenueB
		}
		rows, err := s.gradDB().FundingPredWindow(ctx, bare, g.Symbol, now.Add(-cfg.Window))
		if err != nil {
			return "closed:funding 查询失败 " + err.Error()
		}
		if gate, _, reason := exec.FundingRegimeOK(rows, sign, cfg.FloorPct, cfg.MinSamples, cfg.PulsedAware); !gate {
			return "closed:perp腿 " + reason
		}
		return "open"
	case "perp_perp":
		if g.VenueA == "" || g.VenueB == "" {
			return "closed:venue 配对臂缺失"
		}
		// venue=shortV×longV（方向在键）：buy=venue_b 长腿 sell=venue_a 短腿。
		if ok, reason := exec.CarryFundingRegimeCheck(ctx, s.gradDB(), keydomain.Join(g.VenueB, "perp"), keydomain.Join(g.VenueA, "perp"), g.Symbol, g.Symbol, now, cfg); !ok {
			return "closed:" + reason
		}
		return "open"
	case "cfd_cfd":
		return "open" // mt×mt 无 funding 腿（swap 域非本闸覆盖）
	default: // cfd
		if g.MtVenue == nil {
			return "closed:mt_venue 缺失"
		}
		platform, err := s.mtPlatformFor(ctx, *g.MtVenue)
		if err != nil {
			return "closed:" + err.Error()
		}
		mappings, err := s.gradDB().ListSymbolMappings(ctx, *g.MtVenue)
		if err != nil {
			return "closed:broker_symbol_map 读取失败"
		}
		equiv, eqErr := s.gradDB().ListAssetEquiv(ctx)
		if eqErr != nil { // D-043：降级 nil=legacy 解析（class 行 resolver 自会 fail-closed）
			log.Printf("[graduation] asset_equiv read: %v", eqErr)
		}
		pair, err := scan.ResolveGraduationCanonicals(g.Symbol, *g.MtVenue, g.VenueB, platform, symbolMapEntries(mappings), equiv)
		if err != nil {
			return "closed:" + err.Error()
		}
		vb, vs := pair.MtVenueKey, cexVenueKeyOf(g.VenueB)
		cb, cs := pair.MtCanonical, pair.CexCanonical
		if g.Structure == "short_cfd_long_perp" {
			vb, vs = vs, vb
			cb, cs = cs, cb
		}
		if ok, reason := exec.CarryFundingRegimeCheck(ctx, s.gradDB(), vb, vs, cb, cs, now, cfg); !ok {
			return "closed:" + reason
		}
		return "open"
	}
}
