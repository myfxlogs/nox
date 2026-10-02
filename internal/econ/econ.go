// Package econ —— D-014：经济闸单结构，一个结构一处计算。
// 五字段全 USD；派生 BreakevenDays/NetIfClosedNow；入场闸与收割闸共用同一函数。
// 装配（读 cashflows/腿/报价/facts）在消费侧——本包只做纯算，禁重复实现。
package econ

import "math"

// Snapshot 持仓/候选入场的经济快照。
type Snapshot struct {
	EntryCostUSD   float64 // 已付入场成本（持仓读 commission cashflows；入场为估算）
	ExitCostEstUSD float64 // 平仓成本估（CEX 腿=notional×taker；MT 腿=实时半点差×qty）
	CarryPerDayUSD float64 // 当前净 carry（年化%×买腿名义/365，线性无衰减）
	CaptureUSD     float64 // basis−anchor 折美元（收敛已捕获部分；入场=0）
	AccruedUSD     float64 // 已落 funding/swap 现金（actual）
}

// BreakevenDays = (EntryCost+ExitCostEst)/CarryPerDay；carry≤0 → +Inf（永不回本）。
func (s Snapshot) BreakevenDays() float64 {
	if s.CarryPerDayUSD <= 0 {
		return math.Inf(1)
	}
	return (s.EntryCostUSD + s.ExitCostEstUSD) / s.CarryPerDayUSD
}

// NetIfClosedNow = Accrued + Capture − EntryCost − ExitCostEst。
func (s Snapshot) NetIfClosedNow() float64 {
	return s.AccruedUSD + s.CaptureUSD - s.EntryCostUSD - s.ExitCostEstUSD
}

// EntryOK D-014 ①入场闸：CarryPerDay>0 ∧ BreakevenDays≤maxBreakeven（缺省 3.0）。
func (s Snapshot) EntryOK(maxBreakevenDays float64) bool {
	return s.CarryPerDayUSD > 0 && s.BreakevenDays() <= maxBreakevenDays
}

// HarvestOK D-014 ②收割闸：NetIfClosedNow≥0 ∧ ≥CarryPerDay×horizonDays。
// NetIfClosedNow<0 永不收割（亏损出场只走 disaster/carry_death）。
func (s Snapshot) HarvestOK(horizonDays float64) bool {
	net := s.NetIfClosedNow()
	return net >= 0 && net >= s.CarryPerDayUSD*horizonDays
}
