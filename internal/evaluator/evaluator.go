// Package evaluator 实现机会评估器（§6）。
// Candidate → 成本拆解 → 净收益 → 保本天数 → 三档判定 → Opportunity。
package evaluator

import (
	"arb/internal/calib"
	"arb/internal/keydomain"
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
	CanonicalSell  string // D-300：卖腿 canonical（跨标的套利；同标的留空）
	AssetClass     string // D-300：标的等价类（同标的机会留空）
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
	CostQuality    string // D-159 W4a：成本质量标记（unknown/estimated/actual），不落 opportunities 表（内部结构）
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
func EvaluateAt(c detector.Candidate, cfg Config, spreadBuy, spreadSell, commissionBps, avg30dBps float64, now time.Time) *Result {
	spreadCost := spreadBuy + spreadSell
	// D-145 §②：滑点 per-leg 计算——MT 腿用 cfg.SlippageBps（0.3 不变，观察期铁证），
	// CEX 腿用 slipBpsForCEX（品种分级表，与 cfg.SlippageBps 解耦）。
	// 纯 MT 组合行为与现状完全一致（0.3×2 回归锚）；混合腿 MT×CEX 不再误伤。
	// D-300 D1/G4：卖腿滑点用 CanonicalSell（跨标的配对 canonical 不同，如 PAXGUSDT 三线 8.0），
	// 空回退 Canonical（同标的机会不变）。
	sellCanonical := c.CanonicalSell
	if sellCanonical == "" {
		sellCanonical = c.Canonical
	}
	slippageCost := slipForLeg(c.VenueBuy, c.Canonical, cfg.SlippageBps) + slipForLeg(c.VenueSell, sellCanonical, cfg.SlippageBps)
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
	case detector.TypeXAssetCarry:
		// D-300 S5（G2 撤回 D4）：息差家族同口径——净年化 pp = gross − 资金机会成本（R1=A 4.5pp）。
		// gross=detector 净息差（funding−swap 已含）；swap_cost 列恒 0——swap 已并入 gross，
		// swap-free venue 由 adapter 源头置 0（bc388c43），Exness 实收=0 语义一致（裁 9）。
		netBps = grossBps - cfg.MarginRateAnnual
		// 一次性摩擦进 breakeven_days：进场价差（有利时为负项缩短回本）+ spread+commission+slip 往返。
		entrySpreadBps := (c.PriceSell - c.PriceBuy) / ((c.PriceBuy + c.PriceSell) / 2) * 10000
		oneTimeCost := (spreadCost+commissionBps+slippageCost)*2 - entrySpreadBps
		dailyNetBps := netBps / 100 * 10000 / 365
		if dailyNetBps > 0 {
			bd := oneTimeCost / dailyNetBps
			breakevenDays = &bd
		}
	}

	rating := classify(c.Type, netBps, c.GrossBps, avg30dBps, cfg.GrabMinNetAnnualized)

	// D-159 W4a：成本质量——commissionBps 来源未知（默认值非真实对拍）→ unknown；
	// 未来真实对拍后改 estimated/actual。当前恒 unknown（保守）。
	costQuality := "unknown"
	qt := time.UnixMilli(c.QuoteTime)
	return &Result{
		ID:             genID(c, qt),
		Type:           string(c.Type),
		Canonical:      c.Canonical,
		CanonicalSell:  c.CanonicalSell,
		AssetClass:     c.AssetClass,
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
		CostQuality:    costQuality,
	}
}

// Evaluate 兼容包装（D-194 §3.3：生产旧入口 = EvaluateAt(time.Now())）。
func Evaluate(c detector.Candidate, cfg Config, spreadBuy, spreadSell, commissionBps, avg30dBps float64) *Result {
	return EvaluateAt(c, cfg, spreadBuy, spreadSell, commissionBps, avg30dBps, time.Now())
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
		// D-159 W4a：avg30dBps=0（无历史基线）→ 降级为 V0/观察，不进 grab（SSOT §5 Phase 3-MT 第 4 点）。
		// 有基线时瞬时 > 基线×2 → grab。
		if netBps > grabThr && avg30dBps > 0 && instantBps > avg30dBps*2 {
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
// D-508 S13 豁免：本表=venue×品种实测标定（非可推导配置），改值/扩行须带新观测证据走 D# 票。
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
	// D-300 裁 4：PAXG 24h $11.6M 流动性 → 三线 8.0（禁回落默认 3.0）
	"PAXGUSDT": 8.0,
	// 9-16 业主并存裁定：XAUT 24h $37.3M（实测）深度更浅 → 同三线 8.0
	"XAUTUSDT": 8.0,
	// D-303：XAUUSDT 金价指数永续——±10bps 盘口 $2M+/$100k taker ≤0.3bps 实测 → 一线 1.0
	"XAUUSDT": 1.0,
	// D-308 雷达量分级，待实盘滑点实测修正：binance 主力所按 24h 量分档。
	"CLUSDT":     2.0, // >$500M
	"BZUSDT":     2.0,
	"XAGUSDT":    2.0,
	"COPPERUSDT": 5.0, // $31M
	"XPTUSDT":    8.0, // <$10M 薄
	"XPDUSDT":    8.0,
}

// D-308 跨所腿与指数薄盘保守全 8.0。键 venue:canonical——ticks canonical 全所同键
// <base>USDT（gate CL_USDT→存 CLUSDT），venue 原生名永不命中，故按 venue 区分。
var cexSlipBpsByVenueCanonical = map[string]float64{
	"gate:XAUUSDT": 8.0, "okx:XAUUSDT": 8.0,
	"gate:CLUSDT": 8.0, "okx:CLUSDT": 8.0,
	"gate:BZUSDT": 8.0, "okx:BZUSDT": 8.0,
	"gate:XAGUSDT": 8.0, "okx:XAGUSDT": 8.0,
	"gate:XPTUSDT": 8.0, "okx:XPTUSDT": 8.0,
	"gate:XPDUSDT": 8.0, "okx:XPDUSDT": 8.0,
	"gate:NAS100USDT": 8.0,
	"gate:US30USDT":   8.0,
	"gate:SPX500USDT": 8.0,
	"okx:JP225USDT":   8.0,
}

// slipBpsForCEX 返回 CEX 品种的单腿滑点 bps（calib active→venue 分级→品种分级→
// cexSlipDefaultBps→calib default）。D-533 S5：calib 命中覆盖分级表（D-508 证据纪律
// 正式接管位）；纯计算热路径走 calib.Lookup 进程缓存（签名链零改动，复审注记）。
func slipBpsForCEX(venueKey, canonical string) float64 {
	venue := keydomain.BareOf(venueKey)
	if v := calib.Lookup("cex_slip_bps", venue, canonical, calib.LookupMiss); v != calib.LookupMiss {
		return v
	}
	if v, ok := cexSlipBpsByVenueCanonical[venue+":"+canonical]; ok {
		return v
	}
	if v, ok := cexSlipBpsByCanonical[canonical]; ok {
		return v
	}
	if v := calib.Lookup("cex_slip_default_bps", "", "", calib.LookupMiss); v != calib.LookupMiss {
		return v
	}
	return cexSlipDefaultBps
}

// slipForLeg 按腿 venueKey 后缀判定滑点：MT（:mt4/:mt5）→ cfg.SlippageBps；CEX（:perp/:spot 等）→ slipBpsForCEX。
// 纯 MT 组合两腿均走 cfg.SlippageBps（0.3×2 回归锚，观察期铁证）。
func slipForLeg(venueKey, canonical string, mtSlipBps float64) float64 {
	if keydomain.IsMTKey(venueKey) {
		return mtSlipBps
	}
	return slipBpsForCEX(venueKey, canonical)
}
