// Package evaluator 实现机会评估器（§6）。
// Candidate → 成本拆解 → 净收益 → 保本天数 → 三档判定 → Opportunity。
package evaluator

import (
	"fmt"
	"strings"
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
	// D-133 T2：息差型回本周期上门（ARB_FUNDING_MAX_BREAKEVEN_DAYS，缺省 1.0）。
	// breakevenDays > 此值 → NetBps 置负，使下游 funding_min% 过滤自然淘汰（不加新淘汰通道）。
	// 0 = 不启用（回本门关闭）。
	FundingMaxBreakevenDays float64
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
// 一次性 bps 且已含开仓方向双边点差（检测器用可执行价计算），但平仓方向 bid-ask 穿透
// 仍需扣——D-113 往返 bid-ask 成本 = half-spread × 4 / mid × 10000（开仓+平仓各吃全宽 spread）。
//
// D-078 round-trip 成本修正：开仓+平仓各付一轮 commission+slippage（+spread for 息差型），
// 旧模型只算开仓成本导致净收益虚高——实证 31 笔 CROSS_VENUE avg gross 46bps 但 avg realized -$1.19。
// D-113 续篇：bid-ask 穿透仍漏算——51 笔只 1 盈累计 -$512，宽 spread venue 组合 net 虚高 21 bps。
func Evaluate(c detector.Candidate, cfg Config, spreadBuy, spreadSell, commissionBps, avg30dBps float64) *Result {
	spreadCost := spreadBuy + spreadSell
	// D-145 §②：滑点 per-leg 计算——MT 腿用 cfg.SlippageBps（0.3 不变，观察期铁证），
	// CEX 腿用 slipBpsForCEX（品种分级表，与 cfg.SlippageBps 解耦）。
	// 纯 MT 组合行为与现状完全一致（0.3×2 回归锚）；混合腿 MT×CEX 不再误伤。
	slippageCost := slipForLeg(c.VenueBuy, c.Canonical, cfg.SlippageBps) + slipForLeg(c.VenueSell, c.Canonical, cfg.SlippageBps)
	grossBps := c.GrossBps

	// 净收益计算（§6.2）
	var netBps float64
	var breakevenDays *float64

	switch c.Type {
	case detector.TypeCrossVenue:
		// D-113：往返 bid-ask 成本 = 双腿全宽 spread × 2（开仓+平仓各吃一次 bid-ask 穿透）
		// spreadBuy/spreadSell 是美元 half-spread → ×4 = 往返全宽；转 bps 对齐 gross
		mid := (c.PriceBuy + c.PriceSell) / 2
		roundTripSpreadBps := (spreadBuy + spreadSell) / mid * 10000 * 4
		// D-078 round-trip：开仓+平仓各付 commission+slippage
		netBps = grossBps - roundTripSpreadBps - (commissionBps+slippageCost)*2
	case detector.TypeFundingHedge, detector.TypeCarry, detector.TypePerpFunding:
		// 息差型：净年化（百分点）= gross − 资金机会成本（D-029/D-036）
		netBps = grossBps - cfg.MarginRateAnnual
		// 一次性摩擦只进 breakeven_days：按净年化推日息差（bps_daily = pct/100×10⁴/365）
		// D-078 round-trip：开仓+平仓各付 spread+commission+slippage
		oneTimeCost := (spreadCost + commissionBps + slippageCost) * 2
		dailyNetBps := netBps / 100 * 10000 / 365
		if dailyNetBps > 0 {
			bd := oneTimeCost / dailyNetBps
			breakevenDays = &bd
		}
		// D-133 T2：回本周期上门——breakevenDays > FundingMaxBreakevenDays → NetBps 置负，
		// 使下游 funding_min% 过滤自然淘汰（评级管道零改动，classify net≤0 恒 trap）。
		// 0 = 不启用（回本门关闭）。breakevenDays nil（dailyNetBps≤0，本就 trap）跳过。
		if cfg.FundingMaxBreakevenDays > 0 && breakevenDays != nil && *breakevenDays > cfg.FundingMaxBreakevenDays {
			netBps = -1
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

// genID 生成机会 ID = 组合键（D-080 S2：TYPE:canonical:venueBuy:venueSell，无时间戳）。
// venue 排序保证同组合不同 tick 产同键（opportunities 表 PK=组合键，Upsert 刷新同行）。
func genID(c detector.Candidate, qt time.Time) string {
	a, b := c.VenueBuy, c.VenueSell
	if a > b {
		a, b = b, a
	}
	return fmt.Sprintf("%s:%s:%s:%s", c.Type, c.Canonical, a, b)
}

// D-145 §②：CEX 滑点品种分级表（与 cfg.SlippageBps 解耦）。
// 依据 D-145 基准快照（$1K 名义模拟撮合，okx 侧实测 max bps）：
// ETH 0.02 / BNB 0.73 / DOT 0.60 / AVAX 2.04 / TRX 3.84 / FIL 1.98 / ATOM 3.36 / NEAR 7.81
// 分级保守取整（一线深度极好→1.0；二线→3.0；三线小币种→8.0；未知→3.0）。
// cfg.SlippageBps（0.3）保持 MT 路径专用，改它会穿透 SWAPARB 判定（违反观察期纪律）。
const cexSlipDefaultBps = 3.0 // 未知 CEX 品种回落值（独立于 cfg.SlippageBps）

var cexSlipBpsByCanonical = map[string]float64{
	// 一线（深度极好）
	"ETHUSDT": 1.0,
	"BNBUSDT": 1.0,
	"BTCUSDT": 1.0,
	// 二线
	"DOTUSDT":  3.0,
	"AVAXUSDT": 3.0,
	"FILUSDT":  3.0,
	"SOLUSDT":  3.0,
	"XRPUSDT":  3.0,
	"ADAUSDT":  3.0,
	// 三线（小币种，okx 侧滑点显著）
	"TRXUSDT":  8.0,
	"ATOMUSDT": 8.0,
	"NEARUSDT": 8.0,
}

// slipBpsForCEX 返回 CEX 品种的单腿滑点 bps（品种表命中→分级值；未知→cexSlipDefaultBps）。
func slipBpsForCEX(canonical string) float64 {
	if v, ok := cexSlipBpsByCanonical[canonical]; ok {
		return v
	}
	return cexSlipDefaultBps
}

// slipForLeg 按腿 venueKey 后缀判定滑点：MT（:mt4/:mt5）→ cfg.SlippageBps；CEX（:perp/:spot 等）→ slipBpsForCEX。
// 纯 MT 组合两腿均走 cfg.SlippageBps（0.3×2 回归锚，观察期铁证）。
func slipForLeg(venueKey, canonical string, mtSlipBps float64) float64 {
	if strings.HasSuffix(venueKey, ":mt4") || strings.HasSuffix(venueKey, ":mt5") {
		return mtSlipBps
	}
	return slipBpsForCEX(canonical)
}
