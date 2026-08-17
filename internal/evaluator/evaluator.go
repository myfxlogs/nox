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
	SlippageBps         float64
	MarginRateAnnual    float64 // 百分点（D-031）
	DetectorFreshnessTTL time.Duration
	OpportunityTTL      time.Duration
}

// Result 评估结果，对应 opportunities 表（§2.6）。
type Result struct {
	ID              string
	Type            string
	VenueBuy        string
	VenueSell       string
	PriceBuy        float64
	PriceSell       float64
	GrossBps        float64
	SpreadCost      float64
	CommissionCost  float64
	SlippageCost    float64
	SwapCost        float64
	NetBps          float64
	BreakevenDays   *float64
	QuoteTime       time.Time
	ExpiresAt       time.Time
	Rating          Rating
}

// Evaluate 评估单个 Candidate（§6.1）。
// spreadBuy/spreadSell 为买/卖腿的 half-spread（(ask-bid)/2）。
// commissionBps 为双腿总佣金 bps。
// swapBps 为息差型年化 bps（价差型传 0）。
// avg30dBps 为 30 日均值（三档判定用，无数据传 0）。
func Evaluate(c detector.Candidate, cfg Config, spreadBuy, spreadSell, commissionBps, swapBps, avg30dBps float64) *Result {
	spreadCost := spreadBuy + spreadSell
	slippageCost := cfg.SlippageBps * 2 // 两腿各 1 bps
	grossBps := c.GrossBps

	// 净收益计算（§6.2）
	var netBps float64
	var breakevenDays *float64

	switch c.Type {
	case detector.TypeCrossVenue:
		// 价差型：一次性 bps
		netBps = grossBps - spreadCost - commissionBps - slippageCost
	case detector.TypeFundingHedge:
		// 息差型：净年化 = funding - margin_rate（D-029）
		netBps = grossBps - cfg.MarginRateAnnual
		// 一次性摩擦只进 breakeven_days（D-029）
		oneTimeCost := spreadCost + commissionBps + slippageCost
		if swapBps != 0 {
			bd := oneTimeCost / (swapBps / 365 * 100) // 日息差 bps
			breakevenDays = &bd
		}
	case detector.TypeCarry:
		// 息差型：净年化 bps（gross 已是年化）
		netBps = grossBps
		oneTimeCost := spreadCost + commissionBps + slippageCost
		dailyNet := grossBps / 365
		if dailyNet > 0 {
			bd := oneTimeCost / dailyNet
			breakevenDays = &bd
		}
	}

	rating := classify(c.Type, netBps, c.GrossBps, avg30dBps)

	now := time.Now()
	qt := time.UnixMilli(c.QuoteTime)
	return &Result{
		ID:             genID(c, qt),
		Type:           string(c.Type),
		VenueBuy:       c.VenueBuy,
		VenueSell:      c.VenueSell,
		PriceBuy:       c.PriceBuy,
		PriceSell:      c.PriceSell,
		GrossBps:       grossBps,
		SpreadCost:     spreadCost,
		CommissionCost: commissionBps,
		SlippageCost:   slippageCost,
		SwapCost:       swapBps,
		NetBps:         netBps,
		BreakevenDays:  breakevenDays,
		QuoteTime:      qt,
		ExpiresAt:      now.Add(cfg.OpportunityTTL),
		Rating:         rating,
	}
}

// classify 三档判定（§6.3）。
func classify(typ detector.OppType, netBps, instantBps, avg30dBps float64) Rating {
	if netBps <= 0 {
		return RatingTrap
	}
	// 价差型用一次性 bps 判定；息差型用年化 bps 判定
	switch typ {
	case detector.TypeCrossVenue:
		if netBps > 0 {
			return RatingGrab
		}
		return RatingTrap
	default:
		// 息差型：net_annualized > 4.5% 且瞬时 > 30日均值×2 → grab
		if netBps > 4.5 && (avg30dBps == 0 || instantBps > avg30dBps*2) {
			return RatingGrab
		}
		if netBps > 0 {
			return RatingBreakeven
		}
		return RatingTrap
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
