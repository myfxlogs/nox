package detector

import (
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/bus"
)

// PerpFundingDetector 检测跨所 perp funding rate 价差（D-064）。
// 同一 canonical 在两个 CEX 的 perp funding rate 不同 → 高 funding 端持空（收）、
// 低 funding 端持多（付），净收 funding 差（年化百分点）。
type PerpFundingDetector struct {
	MinNetAnnualized float64 // 年化净 funding 差门槛（百分点，2.0=2%）
	FreshnessTTL     time.Duration
	// D-154 S3：白名单拒绝首报去重——每 canonical 仅首报一次（进程生命周期；重启重报一次可接受）。
	seenReject sync.Map
}

// D-151 入场白名单：PERP_FUNDING 仅允许 BTC/ETH/TRX 三种 canonical 入场。
// 依据 D-151 费率持续性判定——顺风期 1-2h≪结算窗，小币种磨损重尾风险高；
// BTC/ETH 深度极好+TRX 实证可执行（D-120 #358/#359 双腿 FILLED）。
// 非白名单 canonical 静默跳过（不产候选），首次命中日志注明 D-151。
var perpFundingWhitelist = map[string]bool{
	"BTCUSDT": true,
	"ETHUSDT": true,
	"TRXUSDT": true,
}

func (d *PerpFundingDetector) Type() OppType { return TypePerpFunding }

// Scan 需要 funding Fact（通过 FactProvider 注入）+ QuoteBus snapshot。
// fundingFacts: canonical → venue → 年化百分点（正=多头付空头收，负=反向）。
func (d *PerpFundingDetector) Scan(_ context.Context, snapshot map[string]map[string]bus.Quote, fp FactProvider) []Candidate {
	var out []Candidate
	now := time.Now()
	var fundingFacts map[string]map[string]float64
	var realMap map[string]*bool
	var simMap map[string]bool // D-369-R2 S3：sim 键集（"venue:type"→true，赢家 config 判定）
	if fp != nil {
		fundingFacts = fp.FundingFacts()
		realMap = fp.IsRealMap()
		simMap = fp.SimVenueKeys()
	}
	for canonical, venues := range snapshot {
		// D-151 入场白名单 + D-369 S3 sim 试单通道：非白名单 canonical 不立即跳过——
		// 收集 legs 后，若存在双腿均【已知 sim】（realMap 显式 false）的 venue 对 → 仅该
		// sim 对产候选（demo 轨验证；real/混合对维持原拒）。无全 sim 对 → 维持原跳过
		// +首报日志。白名单 canonical（BTC/ETH/TRX）行为完全不变。
		simOnlyPair := !perpFundingWhitelist[canonical]
		if simOnlyPair {
			if _, loaded := d.seenReject.LoadOrStore(canonical, true); !loaded {
				log.Printf("[perp_funding] D-151 白名单外 canonical=%s：仅双腿已知 sim 产候选（D-369 S3），首报", canonical)
			}
		}
		// 收集所有 perp venue 的报价 + funding
		type perpLeg struct {
			q       bus.Quote
			funding float64
			ok      bool
		}
		var legs []perpLeg
		for _, q := range venues {
			if q.VenueType != "perp" {
				continue
			}
			if stale(q, now, d.FreshnessTTL) {
				continue
			}
			funding, ok := lookupFunding(fundingFacts, canonical, q.Venue)
			if !ok {
				continue
			}
			legs = append(legs, perpLeg{q: q, funding: funding, ok: true})
		}
		// 两两配对（i < j 避免重复 + 同所跳过）
		for i := 0; i < len(legs); i++ {
			for j := i + 1; j < len(legs); j++ {
				a, b := legs[i], legs[j]
				if a.q.Venue == b.q.Venue {
					continue // 同所不算跨所
				}
				// D-178：is_real 配对检查（fail-open，gate 层兜底）。
				// D-369 S3：非白名单 canonical → 仅放行双腿均【已知 sim】的 venue 对
				// D-369-R2 S3：非白名单 canonical → 仅放行双腿均在 sim 键集的 venue 对
				// （simMap["venue:type"]==true=赢家 config 为 sim；缺席=未知→跳过 fail-closed）。
				// 既有 isRealPair/白名单零改动（real 域维持 D-151/D-178 语义）。
				if simOnlyPair {
					if !simMap[a.q.Venue+":"+a.q.VenueType] || !simMap[b.q.Venue+":"+b.q.VenueType] {
						continue
					}
				} else if !isRealPair(realMap, a.q.Venue, b.q.Venue) {
					continue
				}
				// 净年化 = |fundingA - fundingB|（高 funding 端持空收、低 funding 端持多付）
				spread := a.funding - b.funding
				var buyLeg, sellLeg bus.Quote
				if spread > 0 {
					// A funding 高 → A 端持空（sell）、B 端持多（buy）
					buyLeg, sellLeg = b.q, a.q
				} else {
					// B funding 高 → B 端持空（sell）、A 端持多（buy）
					buyLeg, sellLeg = a.q, b.q
					spread = -spread
				}
				// gross = funding 差（年化百分点），evaluator 扣 marginRate 得 net
				if spread < d.MinNetAnnualized {
					continue
				}
				qt := a.q.Time
				if b.q.Time.After(qt) {
					qt = b.q.Time
				}
				out = append(out, Candidate{
					Type:      TypePerpFunding,
					Canonical: canonical,
					VenueBuy:  buyLeg.VenueKey(),
					VenueSell: sellLeg.VenueKey(),
					PriceBuy:  buyLeg.Ask,
					PriceSell: sellLeg.Bid,
					GrossBps:  spread,
					QuoteTime: qt.UnixMilli(),
				})
			}
		}
	}
	return out
}
