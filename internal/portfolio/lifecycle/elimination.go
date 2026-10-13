// elimination.go —— 策略淘汰判定（evaluateElimination 从 gate_decision.go 拆出，D-212 拆搬）。
//
// D-260 M4.2：判据迁机读表 fin_strategy_criteria（kind='elimination'），
// 源码零阈值字面量——改行即改判据，验收=判据表改行不改码变行为。
// 种子行=原字面量语义逐项对等（D-136 小样本双负双条件 / ≥30 大单条件）。
package lifecycle

import (
	"context"
	"fmt"
	"log"
)

// EliminationInput evaluateElimination 纯函数入参。
// 加 MedianPnl（小样本判据 median<0 AND avg<0，防 funding 垫正虚高）。
type EliminationInput struct {
	TotalTrades int
	WinRate     float64
	AvgPnl      float64
	MedianPnl   float64
	Disabled    bool
}

// EliminationMetrics 输入投影为谓词 metrics（名=判据 conds 引用词面）。
func (in EliminationInput) EliminationMetrics() map[string]float64 {
	return map[string]float64{
		"total_trades": float64(in.TotalTrades),
		"win_rate":     in.WinRate,
		"avg_pnl":      in.AvgPnl,
		"median_pnl":   in.MedianPnl,
	}
}

// EvaluateElimination 策略淘汰判定（判据行驱动——D-260）。
// crits=fin_strategy_criteria kind='elimination' 行（'*' 全局或策略专属，专属+全局同评）。
// 语义锚不变：funding/swap 垫正只能把少数仓推过零线（winRate 虚高），中位数暴露真相。
// 判据表空=无规则→不杀但 caller 日志可见（返回 reason 空）。
func EvaluateElimination(in EliminationInput, crits []Criterion) (disable bool, reason string) {
	if in.Disabled {
		return false, ""
	}
	fire, hits := FireCriteria(in.EliminationMetrics(), crits)
	if !fire {
		return false, ""
	}
	return true, fmt.Sprintf("auto-disabled[%s]: %d trades, winRate=%.0f%%, median=$%.2f, avg=$%.2f",
		fmtHits(hits), in.TotalTrades, in.WinRate*100, in.MedianPnl, in.AvgPnl)
}

func fmtHits(hits []string) string {
	s := ""
	for i, h := range hits {
		if i > 0 {
			s += "+"
		}
		s += h
	}
	return s
}

// EliminatorLoader 判据加载面（ctx+kind→行集；pgstore.ListStrategyCriteria 直接适配）。
type EliminatorLoader func(ctx context.Context, kind string) ([]Criterion, error)

// NewEliminator 构造 settlement.Eliminator——每次判定实时读判据表（改行即生效）。
// D-087 §3.2 装配点；判据读败/空表→不杀（fail-open 侧=log 可见，防误杀全面停策略）。
func NewEliminator(load EliminatorLoader) func(ctx context.Context, totalTrades int, winRate, avgPnl, medianPnl float64, disabled bool) (bool, string) {
	return func(ctx context.Context, totalTrades int, winRate, avgPnl, medianPnl float64, disabled bool) (bool, string) {
		crits, err := load(ctx, "elimination")
		if err != nil {
			log.Printf("[lifecycle] elimination criteria load: %v（本轮不淘汰）", err)
			return false, ""
		}
		if len(crits) == 0 {
			log.Printf("[lifecycle] elimination criteria 空表——无规则不淘汰")
			return false, ""
		}
		return EvaluateElimination(EliminationInput{
			TotalTrades: totalTrades,
			WinRate:     winRate,
			AvgPnl:      avgPnl,
			MedianPnl:   medianPnl,
			Disabled:    disabled,
		}, crits)
	}
}
