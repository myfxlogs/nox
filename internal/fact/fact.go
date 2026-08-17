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
	KindFunding    = "funding"     // 永续资金费率（UnitPctAnnualized）
	KindDefiRate   = "defi_rate"   // 链上/DeFi 收益利率（UnitPctAnnualized）
	KindFX         = "fx"          // 汇率 USDCNH（UnitPrice）
	KindSwapLong   = "swap_long"   // MT 多单 swap（UnitBpsDaily，D-028 归一）
	KindSwapShort  = "swap_short"  // MT 空单 swap（UnitBpsDaily，D-028 归一）
)

// Unit 常量表（§1.2）。
const (
	UnitPctAnnualized = "pct_annualized" // 年化百分点（5.0 = 5%，D-031）
	UnitPrice         = "price"          // 价格（如 USDCNH 7.25）
	UnitBpsDaily      = "bps_daily"      // 日 bps（swap 归一，D-028）
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
	KindFunding:   true,
	KindDefiRate:  true,
	KindFX:        true,
	KindSwapLong:  true,
	KindSwapShort: true,
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
