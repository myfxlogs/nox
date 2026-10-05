// carry_universe_suspect.go D-345：suspect 判定（输入腿年化疑源线，自 carry_universe.go
// 整组移出）。排名卫生与告警排除同族。
// D-072：原 D-336 S3 真候选阈值告警已退役——发现层产物经 D-050 自动进纸面，
// 决策面读毕业器/Stage 表。
package scan

import (
	"fmt"
	"strings"
)

// SUSPECT_PCT D-345：输入腿年化疑源线（%）。DOT 脏源实证（broker 1314615 DOTUSDT
// swap_long=−469.5%/yr 离谱定价致假机会霸榜=发现层失能）：越界行不删（保观测），
// net=0+swap_free=false+note 剔除排名，digest/告警自然排除。
const SUSPECT_PCT = 100.0

// suspectNote D-345：任一输入腿 |年化|>SUSPECT_PCT → 返回疑因 note；干净组合返回 ""。
func suspectNote(swapPct, fundingPct float64) string {
	parts := make([]string, 0, 2)
	if abs(swapPct) > SUSPECT_PCT {
		parts = append(parts, fmt.Sprintf("swap %.1f%%", abs(swapPct)))
	}
	if abs(fundingPct) > SUSPECT_PCT {
		parts = append(parts, fmt.Sprintf("funding %.1f%%", abs(fundingPct)))
	}
	if len(parts) == 0 {
		return ""
	}
	return "suspect: " + strings.Join(parts, ", ")
}
