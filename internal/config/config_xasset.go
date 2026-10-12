// config_xasset.go —— D-479 S1：XASSET_CARRY 字段族自 config_types.go 整段搬移
// （嵌入 Config，访问 cfg.XassetXxx 不变；纯搬移零逻辑变更）。
package config

// XassetConfig D-300 跨标的同质资产套利参数。
type XassetConfig struct {
	XassetEntryMinBps      float64 // 进场价差门槛（bps，负值=容忍逆价进场；ARB_XASSET_ENTRY_MIN_BPS）
	XassetMinNetAnnualPct  float64 // 净息差门槛（funding−swap 年化 pp；ARB_XASSET_MIN_NET_ANNUAL_PCT）
	XassetTrackingMaxBps   float64 // 跟踪误差上限（bps，设计 §5.3=50；ARB_XASSET_TRACKING_MAX_BPS）
	XassetCarryMinNetStore float64 // ARB_XASSET_CARRY_MIN_NET_STORE（默认 0=不过滤；detector 净息差门+evaluator margin 已双道）
}
