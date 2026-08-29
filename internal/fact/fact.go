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
	KindFunding        = "funding"          // 永续资金费率（UnitPctAnnualized）
	KindDefiRate       = "defi_rate"        // 链上/DeFi 收益利率（UnitPctAnnualized）
	KindFX             = "fx"               // 汇率 USDCNH（UnitPrice）
	KindSwapLong       = "swap_long"        // MT 多单 swap（UnitBpsDaily，D-028 归一）
	KindSwapShort      = "swap_short"       // MT 空单 swap（UnitBpsDaily，D-028 归一）
	KindTradeMode      = "trade_mode"       // MT5 符号可交易性（UnitMode，D-060 F3）
	KindTripleSwapDay  = "triple_swap_day"  // MT 三倍 swap 日（UnitWeekday，D-077 V2）
	KindInitMarginRate = "init_margin_rate" // MT5 初始保证金率（UnitRatio，S6 返修：账户级=1/杠杆，无限杠杆=0）
	KindOptParityDev   = "opt_parity_dev"   // 期权平价偏离 bps（UnitBps，D-137 Deribit 采集监控）
	KindMTSymbolMeta   = "mt_symbol_meta"   // MT5 符号元数据（UnitContractSize，D-140 S2 CS 自动来源）
	KindMTSymbolMargin = "mt_symbol_margin" // MT5 品种级保证金率（UnitRatio，D-142 ③ 品种级优先账户级回落）
)

// Unit 常量表（§1.2）。
const (
	UnitPctAnnualized = "pct_annualized" // 年化百分点（5.0 = 5%，D-031）
	UnitPrice         = "price"          // 价格（如 USDCNH 7.25）
	UnitBpsDaily      = "bps_daily"      // 日 bps（swap 归一，D-028）
	UnitMode          = "mode"           // trade_mode 值域（D-060 F3）
	UnitWeekday       = "weekday"        // 星期几（0=Sunday...6=Saturday，对齐 Go time.Weekday，D-077 V2）
	UnitRatio         = "ratio"          // 小数比率（0.01=1%，S6 保证金率）
	UnitBps           = "bps"            // 基点（1bp=0.01%，期权平价偏离 D-137）
	UnitContractSize  = "contract_size"  // 合约规格（D-140 S2，MT5 SymbolInfo.ContractSize）
)

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
	KindFunding:        true,
	KindDefiRate:       true,
	KindFX:             true,
	KindSwapLong:       true,
	KindSwapShort:      true,
	KindTradeMode:      true,
	KindTripleSwapDay:  true,
	KindInitMarginRate: true,
	KindOptParityDev:   true,
	KindMTSymbolMeta:   true,
	KindMTSymbolMargin: true,
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
