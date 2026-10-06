// Package fact 定义统一事实模型（§1.2）。
// 非报价类数据（funding rate、DeFi 利率、汇率、swap）沿用 Fact 模型。
// 规则引擎只认 Fact 不认来源；Value 口径由 Kind 约定。
package fact

import (
	"fmt"
	"time"
)

// Kind 常量表（§1.2）。
const (
	KindFunding           = "funding"             // 永续资金费率（UnitPctAnnualized）
	KindFundingNextSettle = "funding_next_settle" // 永续下次结算时刻（UnitUnixSec，D-466 venue 原生值）
	KindFundingInterval   = "funding_interval"    // 永续结算周期秒数（UnitSeconds，D-466 venue 原生值）
	KindFundingHistory    = "funding_history"     // 永续 funding 结算历史（UnitPctAnnualized，D-172 S1 结算费率非轮询预测值）
	KindDefiRate          = "defi_rate"           // 链上/DeFi 收益利率（UnitPctAnnualized）
	KindFX                = "fx"                  // 汇率 USDCNH（UnitPrice）
	KindSwapLong          = "swap_long"           // MT 多单 swap（UnitBpsDaily，D-028 归一）
	KindSwapShort         = "swap_short"          // MT 空单 swap（UnitBpsDaily，D-028 归一）
	KindSwapSettledLong   = "swap_settled_long"   // D-180 S1：实测结算画像多单（UnitUsdLotDay，swap_snapshots 跳变聚合中位数）
	KindSwapSettledShort  = "swap_settled_short"  // D-180 S1：实测结算画像空单（UnitUsdLotDay，swap_snapshots 跳变聚合中位数）
	KindTradeMode         = "trade_mode"          // MT5 符号可交易性（UnitMode，D-060 F3）
	KindTradeSession      = "trade_session"       // D-029：MT5 周交易会话表（value=day*1e8+startMin*1e4+endMin，服务器本地→UTC 换算后）
	KindTripleSwapDay     = "triple_swap_day"     // MT 三倍 swap 日（UnitWeekday，D-077 V2）
	KindInitMarginRate    = "init_margin_rate"    // MT5 初始保证金率（UnitRatio，S6 返修：账户级=1/杠杆，无限杠杆=0）
	KindOptParityDev      = "opt_parity_dev"      // 期权平价偏离 bps（UnitBps，D-137 Deribit 采集监控）
	KindMTSymbolMeta      = "mt_symbol_meta"      // MT5 符号元数据（UnitContractSize，D-140 S2 CS 自动来源）
	KindMTSymbolMargin    = "mt_symbol_margin"    // MT5 品种级保证金率（UnitRatio，D-142 ③ 品种级优先账户级回落）
	KindMTSymbolProduct   = "mt_symbol_product"   // MT5 CalcMode 产品类型（UnitCalcMode，D-173 product_group 反哺）
	KindCMCPrice          = "cmc_price"           // CMC 实时报价（UnitPrice，D-205）
	KindCMCAccount        = "cmc_account"         // CMC 账户摘要（UnitUSD，D-205）
	KindCMCHistory        = "cmc_history"         // CMC 历史记录/swap（UnitUSD，D-205）
)

// Unit 常量表（§1.2）。
const (
	UnitPctAnnualized = "pct_annualized" // 年化百分点（5.0 = 5%，D-031）
	UnitPrice         = "price"          // 价格（如 USDCNH 7.25）
	UnitBpsDaily      = "bps_daily"      // 日 bps（swap 归一，D-028）
	UnitUsdLotDay     = "usd_lot_day"    // D-180 S1：USD/手/日（swap_settled 画像口径）
	UnitMode          = "mode"           // trade_mode 值域（D-060 F3）
	UnitWeekday       = "weekday"        // 星期几（0=Sunday...6=Saturday，对齐 Go time.Weekday，D-077 V2）
	UnitRatio         = "ratio"          // 小数比率（0.01=1%，S6 保证金率）
	UnitBps           = "bps"            // 基点（1bp=0.01%，期权平价偏离 D-137）
	UnitContractSize  = "contract_size"  // 合约规格（D-140 S2，MT5 SymbolInfo.ContractSize）
	UnitCalcMode      = "calc_mode"      // CalcMode enum 数值（D-173，MT5 产品类型）
	UnitUSD           = "usd"            // USD 金额（D-205 CMC 账户/历史）
	UnitUnixSec       = "unix_sec"       // unix 秒时间戳（D-466 结算时刻）
	UnitSeconds       = "seconds"        // 秒数时长（D-466 结算周期）
)

// D-591 S6：!ticker@arr 24hrTicker 落库 kind（quote volume，radar 全量宇宙资格判定源）。
const KindVol24h = "vol24h"

// KindIndexPrice D-591 R2：markPrice 帧 i 字段（index price）同帧落库——一帧一钟
// （stream-field-one-clock-law：basis=mark−index 必须同帧同钟，禁 WS mark×REST index 拼接）。
const KindIndexPrice = "index_price"

// TradeMode 值域（MT5 ENUM_SYMBOL_TRADE_MODE 透传，D-060 F4/F5 消费；
// MT4 无此字段不发 Fact → 下游未知放行）。
const (
	TradeModeDisabled  = 0 // 禁交易
	TradeModeLongOnly  = 1 // 只可开多
	TradeModeShortOnly = 2 // 只可开空
	TradeModeCloseOnly = 3 // 只可平仓
	TradeModeFull      = 4 // 双向可开
)

// Fact 是采集 → 归一 → 规则/检测的唯一数据载体（§1.2）。
type Fact struct {
	Kind   string
	Venue  string
	Symbol string
	Value  float64
	Unit   string
	Ts     time.Time
	Src    string
}

var validKinds = map[string]bool{
	KindFunding:           true,
	KindFundingNextSettle: true,
	KindFundingInterval:   true,
	KindFundingHistory:    true,
	KindDefiRate:          true,
	KindFX:                true,
	KindSwapLong:          true,
	KindSwapShort:         true,
	KindSwapSettledLong:   true,
	KindSwapSettledShort:  true,
	KindTradeMode:         true,
	KindTradeSession:      true,
	KindTripleSwapDay:     true,
	KindInitMarginRate:    true,
	KindOptParityDev:      true,
	KindMTSymbolMeta:      true,
	KindMTSymbolMargin:    true,
	KindMTSymbolProduct:   true,
	KindCMCPrice:          true,
	KindCMCAccount:        true,
	KindCMCHistory:        true,
	// D-578：三所 spot 深度采样三 kind（src=cex_depth_sampler）。
	"spot_depth_spread_bps": true,
	"spot_depth_bid_usd":    true,
	"spot_depth_ask_usd":    true,
}

// ValidKind 报告 k 是否为已知 Kind。
func ValidKind(k string) bool { return validKinds[k] }

// Validate 校验 Fact；未知 Kind 拒绝。
func (f Fact) Validate() error {
	if !ValidKind(f.Kind) {
		return fmt.Errorf("fact: unknown kind %q", f.Kind)
	}
	return nil
}

// CalcModeToProductGroup D-173：MT5 CalcMode → product_group 映射。
// CFD(2) 现货与 CFDLeverage(4) 杠杆分属不同 product_group（470 事故根因：报价基准差 ~270bps）。
// 未知 CalcMode → ""（空，配对时放行）。
// 放 fact 包导出：autotrade（单测）与 cmd/core（meta-sink 反哺）共用，P3 单一真相源。
func CalcModeToProductGroup(calcMode int32) string {
	switch calcMode {
	case 0:
		return "fx"
	case 1, 33, 34:
		return "fut"
	case 2:
		return "cfd_spot"
	case 3:
		return "cfd_index"
	case 4:
		return "cfd_leverage"
	default:
		return ""
	}
}
