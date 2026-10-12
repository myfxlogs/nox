// elimination.go —— 策略淘汰判定（evaluateElimination 从 gate_decision.go 拆出，D-212 拆搬）。
package lifecycle

import "fmt"

// EliminationInput evaluateElimination 纯函数入参。
// 加 MedianPnl（小样本判据 median<0 AND avg<0，防 funding 垫正虚高）。
type EliminationInput struct {
	TotalTrades int
	WinRate     float64
	AvgPnl      float64
	MedianPnl   float64
	Disabled    bool
}

// eliminationSampleFloor 小样本噪音保护门槛（D-136）。
// 依据：PERP_FUNDING 日频 ~19 笔，30 笔≈1.5 天即再评估，兼顾灵敏度与噪音保护。
// <此值时单条件（avgPnl<0 或 胜率<30%）易被小样本噪音触发误杀高频微利策略。
const eliminationSampleFloor = 30

// EvaluateElimination 策略淘汰判定（从 updateStrategyStat 抽出，设计 §4.3；D-136 判据分级；D-154 S2 中位数判据）。
// 样本量分级：
//
//	<30 笔（小样本噪音保护）：medianPnl<0 且 avgPnl<0 → 禁用（双条件防误杀；winRate 移出——funding/swap 垫正虚高免疫）
//	≥30 笔（大样本，大数定律）：胜率<30% 或 avgPnl<0 任一 → 禁用（单条件可信）
//
// 语义：funding/swap 垫正只能把少数仓推过零线（winRate 虚高），中位数暴露收益结构真相。
// 三锚：D-136 误杀先例（7 笔 winRate=60% avg=-$0.05）多数仓净正→median>0→不杀 ✓；
// PERP_FUNDING 实证（53 笔 33% 胜率 9 负 3 微正）median<0 且 avg<0→杀 ✓；
// SWAPARB 金鹅（swap 日收 net 多数正）median>0→不杀 ✓。
// 返回 disable + reason。
func EvaluateElimination(in EliminationInput) (disable bool, reason string) {
	if in.Disabled {
		return false, ""
	}
	if in.TotalTrades < 5 {
		return false, ""
	}
	if in.TotalTrades < eliminationSampleFloor {
		// 小样本：median+avg 双负才杀（D-154 S2，winRate 移出——funding 垫正虚高免疫）
		if in.MedianPnl < 0 && in.AvgPnl < 0 {
			return true, fmt.Sprintf("auto-disabled: %d trades, median=$%.2f, avg=$%.2f", in.TotalTrades, in.MedianPnl, in.AvgPnl)
		}
		return false, ""
	}
	// 大样本：单条件（大数定律，均值/胜率可信）
	if in.WinRate < 0.3 || in.AvgPnl < 0 {
		return true, fmt.Sprintf("auto-disabled: %d trades, winRate=%.0f%%, avg=$%.2f", in.TotalTrades, in.WinRate*100, in.AvgPnl)
	}
	return false, ""
}

// EliminatorAdapter 把 evaluateElimination 包装成 settlement.Eliminator（D-087 §3.2）。
// 供 main 装配时注入 settlement.Settle 的 elim 参数。
// 签名同步扩展 medianPnl 入参（settlement.Eliminator 类型已改）。
func EliminatorAdapter(totalTrades int, winRate, avgPnl, medianPnl float64, disabled bool) (bool, string) {
	return EvaluateElimination(EliminationInput{
		TotalTrades: totalTrades,
		WinRate:     winRate,
		AvgPnl:      avgPnl,
		MedianPnl:   medianPnl,
		Disabled:    disabled,
	})
}
