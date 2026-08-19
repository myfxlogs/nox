// Package evaluator 实现机会评估器（§6）。
// Candidate → 成本拆解 → 净收益 → 保本天数 → 三档判定 → Opportunity。
package evaluator

import (
	"fmt"
	"time"

	"arb/internal/detector"
)

// Rating 三档判定（§6.3）。
type Rating string

const (
	RatingGrab      Rating = "grab"
	RatingBreakeven Rating = "breakeven"
	RatingTrap      Rating = "trap"
)

// Config 评估器配置（§12）。
type Config struct {
	SlippageBps          float64
	MarginRateAnnual     float64 // 百分点（D-031）
	DetectorFreshnessTTL time.Duration
	OpportunityTTL       time.Duration
	GrabMinNetAnnualized float64 // grab 档门槛（百分点，D-036，替代硬编码 4.5）
}

// Result 评估结果，对应 opportunities 表（§2.6）。
type Result struct {
	ID             string
	Type           string
	Canonical      string
	VenueBuy       string
	VenueSell      string
	PriceBuy       float64
	PriceSell      float64
	GrossBps       float64
	SpreadCost     float64
	CommissionCost float64
	SlippageCost   float64
	SwapCost       float64
	NetBps         float64
	BreakevenDays  *float64
	QuoteTime      time.Time
	ExpiresAt      time.Time
	Rating         Rating
}

// Evaluate 评估单个 Candidate（§6.1）。
// spreadBuy/spreadSell 为买/卖腿的 half-spread（(ask-bid)/2）。
// commissionBps 为双腿总佣金 bps。
// avg30dBps 为 30 日均值（三档判定用，无数据传 0）。
//
// D-036 单位约定：息差型（carry/funding）的 gross/net 为年化百分点（5.0=5%，D-031），
// 一次性成本（点差/佣金/滑点）为 bps，只进 breakeven_days。价差型（cross）的 gross 为
// 一次性 bps 且已含双边点差（检测器用可执行价计算）→ 净收益不再扣 spread（D-035 双重计算修正）。
func Evaluate(c detector.Candidate, cfg Config, spreadBuy, spreadSell, commissionBps, avg30dBps float64) *Result {
	spreadCost := spreadBuy + spreadSell
	slippageCost := cfg.SlippageBps * 2 // 两腿各 1 bps
	grossBps := c.GrossBps

	// 净收益计算（§6.2）
	var netBps float64
	var breakevenDays *float64

	switch c.Type {
	case detector.TypeCrossVenue:
		// 价差型：一次性 bps，gross 已含双边点差 → 不再扣 spread（D-035）
		netBps = grossBps - commissionBps - slippageCost
	case detector.TypeFundingHedge, detector.TypeCarry:
		// 息差型：净年化（百分点）= gross − 资金机会成本（D-029/D-036）
		netBps = grossBps - cfg.MarginRateAnnual
		// 一次性摩擦只进 breakeven_days：按净年化推日息差（bps_daily = pct/100×10⁴/365）
		oneTimeCost := spreadCost + commissionBps + slippageCost
		dailyNetBps := netBps / 100 * 10000 / 365
		if dailyNetBps > 0 {
			bd := oneTimeCost / dailyNetBps
			breakevenDays = &bd
		}
	}

	rating := classify(c.Type, netBps, c.GrossBps, avg30dBps, cfg.GrabMinNetAnnualized)

	now := time.Now()
	qt := time.UnixMilli(c.QuoteTime)
	return &Result{
		ID:             genID(c, qt),
		Type:           string(c.Type),
		Canonical:      c.Canonical,
		VenueBuy:       c.VenueBuy,
		VenueSell:      c.VenueSell,
		PriceBuy:       c.PriceBuy,
		PriceSell:      c.PriceSell,
		GrossBps:       grossBps,
		SpreadCost:     spreadCost,
		CommissionCost: commissionBps,
		SlippageCost:   slippageCost,
		SwapCost:       0,
		NetBps:         netBps,
		BreakevenDays:  breakevenDays,
		QuoteTime:      qt,
		ExpiresAt:      now.Add(cfg.OpportunityTTL),
		Rating:         rating,
	}
}

// classify 三档判定（§6.3）。
// grabThr 为息差型 grab 档门槛（百分点，D-036 入参化）。net ≤ 0 恒为 trap（不进任何视图）。
func classify(typ detector.OppType, netBps, instantBps, avg30dBps, grabThr float64) Rating {
	if netBps <= 0 {
		return RatingTrap
	}
	// 价差型用一次性 bps 判定；息差型用年化百分点判定
	switch typ {
	case detector.TypeCrossVenue:
		return RatingGrab
	default:
		// 息差型：net_annualized > grabThr 且瞬时 > 30日均值×2 → grab
		if netBps > grabThr && (avg30dBps == 0 || instantBps > avg30dBps*2) {
			return RatingGrab
		}
		return RatingBreakeven
	}
}

// genID 生成机会 ID（§2.6）。
func genID(c detector.Candidate, qt time.Time) string {
	a, b := c.VenueBuy, c.VenueSell
	if a > b {
		a, b = b, a
	}
	return fmt.Sprintf("%s:%s:%s:%s:%d", c.Type, c.Canonical, a, b, qt.UnixMilli())
}
