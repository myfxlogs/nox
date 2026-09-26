// service_carry_graduation_observe.go D-475a S5：质量杆观测即时算（零落库零拒批）。
// 从 service_carry_graduation.go 拆出保 300 软线——行数治理纯搬家，零逻辑变更。
package dashboard

import (
	"context"
	"time"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/fact"
	"arb/internal/keydomain"
	"arb/internal/scan"
	"arb/internal/store/pgstore"
)

// graduationObservability D-475a S5：pending 行质量杆观测即时算（venue_gaps 同模式
// 零落库）。accrue_ticks/min_accrued 直读纸面行；expected_periods=elapsed/
// funding_interval（interval 活读 facts——候选键=行 accrue 腿口径拆段 venue×
// {symbol, symbolUSDT, symbol-USDT-SWAP}，全缺→expected NULL 诚实缺）；
// coverage=accrue_ticks/expected。零拒批——先采数后定阈。
func (s *Service) graduationObservability(ctx context.Context, g *pgstore.CarryGraduation, row *v1.CarryGraduationRow) {
	p, err := s.gradDB().GetPaperSpotCarryByID(ctx, g.PaperID)
	if err != nil {
		return // 行读不出=观测缺（不拦列表）
	}
	ticks := int64(p.AccrueTicks)
	row.AccrueTicks = &ticks
	row.MinAccruedUsd = p.MinAccruedUSD
	// funding_interval 候选键：行 accrue 腿符号口径（spot=venue+symbol 直查；
	// cfd/perp_perp=×拆段 venue×symbol 两形态）。
	elapsed := time.Since(p.EntryTs)
	var ivl float64
	for _, v := range scan.FlagVenueSet(p.Venue, p.MtVenue) {
		bare := keydomain.BareOf(v) // venue_key→裸名（facts 键=裸 venue）
		for _, sym := range []string{p.Symbol, p.Symbol + "USDT", p.Symbol + "-USDT-SWAP"} {
			fs, err := s.gradDB().LatestFacts(ctx, fact.KindFundingInterval, bare, sym)
			if err != nil || len(fs) == 0 {
				continue
			}
			if fs[0].Value > 0 && (ivl == 0 || fs[0].Value < ivl) {
				ivl = fs[0].Value // 双腿不同周期取小者=更严 coverage
			}
			break
		}
	}
	if ivl <= 0 {
		return // interval 缺→expected 诚实缺
	}
	expected := int64(elapsed.Seconds() / ivl)
	row.ExpectedPeriods = &expected
	if expected > 0 {
		cov := float64(p.AccrueTicks) / float64(expected)
		row.CoverageRatio = &cov
	}
}
