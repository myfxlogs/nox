// Package mmm Ralph Vince《The Mathematics of Money Management》方法论数学面
// （zh.md 公式编号直查；D-180 只武装证据字段，kill 扳机数学化属禁区）。
package mmm

import (
	"fmt"
	"math"
)

// BinomialLowerBound (B.12)：N 笔胜率 p 在 Zσ 下的置信下界（只管胜负二元不管幅度）。
func BinomialLowerBound(p float64, n int, z float64) float64 {
	if n < 2 {
		return 0
	}
	return p - z*math.Sqrt(p*(1-p)/float64(n-1))
}

// BinomialNNeeded (B.12) 反解：置信下界越过 target 所需最少 N
// （N = 1 + z²·p(1−p)/(p−target)²）。p≤target 时永远不可达→-1；p≥1 时方差 0 即达→1。
func BinomialNNeeded(p, target, z float64) int {
	if p >= 1 {
		return 1
	}
	if p <= target {
		return -1
	}
	d := p - target
	n := 1 + z*z*p*(1-p)/(d*d)
	return int(math.Ceil(n))
}

// TurningPoints (C.01/C.02) 转折点检验：seq=按时间序的盈亏序列（只看符号）；
// 返回观测转折点数、期望数、标准差。N<3 时期望/方差公式无意义→sd=0。
func TurningPoints(seq []float64) (obs int, expected, sd float64) {
	n := len(seq)
	if n < 3 {
		return 0, 0, 0
	}
	sign := func(v float64) int {
		if v > 0 {
			return 1
		}
		if v < 0 {
			return -1
		}
		return 0
	}
	for i := 1; i < n-1; i++ {
		a, b, c := sign(seq[i-1]), sign(seq[i]), sign(seq[i+1])
		if (b > a && b > c) || (b < a && b < c) {
			obs++
		}
	}
	expected = 2.0 / 3.0 * float64(n-2)     // (C.01)
	sd = math.Sqrt(float64(16*n-29) / 90.0) // (C.02)
	return obs, expected, sd
}

// ElimEvidence 淘汰证据后缀（纯证据非判据）：winRate@n+Zσ 置信下界+距宣判还差笔数。
// target=现行胜率判线（0.3）；n<2 或下界公式无意义时如实标。
func ElimEvidence(total int, winRate, target, z float64) string {
	if total < 2 {
		return fmt.Sprintf("；证据字段 n=%d 不足", total)
	}
	l := BinomialLowerBound(winRate, total, z)
	s := fmt.Sprintf("；winRate %.0f%% @n=%d，%.0fσ 置信下界 %.1f%%", winRate*100, total, z, l*100)
	if need := BinomialNNeeded(winRate, target, z); need < 0 {
		s += fmt.Sprintf("；下界越 %.0f%% 线不可达（胜率≤判线）", target*100)
	} else {
		s += fmt.Sprintf("；距下界越 %.0f%% 线还需 %d 笔", target*100, need-total)
	}
	return s
}
