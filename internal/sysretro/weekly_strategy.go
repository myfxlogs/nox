// weekly_strategy.go 周报§3 策略表+证据行渲染（D-180 拆出保行数闸）。
package sysretro

import (
	"fmt"
	"strings"

	"arb/internal/mmm"
	"arb/internal/store/pgstore"
)

// strategyEvidence D-180：策略行下两行证据（影子裁决+转折点检验），纯证据不入判据。
// shadow=现行态 vs B.12 置信下界判据（30% 胜率线）对拍，分歧标 ⚠；
// dep=转折点检验 Z 分（同类生同类/异类生异类，|Z|>zLim 标异常）。
func strategyEvidence(s pgstore.RetroStrategyRow, seq []float64, zSigma, zLim float64) string {
	cur := "存活"
	if s.Disabled {
		cur = "DISABLED"
	}
	shadow, diverge := "证据不足(<30)", false
	if s.Trades >= 30 {
		l := mmm.BinomialLowerBound(s.WinRate, s.Trades, zSigma)
		if l > 0.30 {
			shadow = fmt.Sprintf("存活（下界%.1f%%）", l*100)
			diverge = s.Disabled
		} else {
			shadow = fmt.Sprintf("支持判死（下界%.1f%%）", l*100)
			diverge = !s.Disabled
		}
	}
	div := ""
	if diverge {
		div = " ⚠分歧"
	}
	line := fmt.Sprintf("  shadow: 现行=%s vs 置信界=%s%s\n", cur, shadow, div)
	if len(seq) < 3 {
		return line + "  dep: 序列不足（n<3）\n"
	}
	obs, exp, sd := mmm.TurningPoints(seq)
	z, note := 0.0, "无显著依赖"
	if sd > 0 {
		z = (float64(obs) - exp) / sd
	}
	if sd > 0 && (z > zLim || z < -zLim) {
		if z < 0 {
			note = "同类生同类（异常）"
		} else {
			note = "异类生异类（异常）"
		}
	}
	return line + fmt.Sprintf("  dep: 转折点 %d/E=%.0f Z=%+.1f %s\n", obs, exp, z, note)
}

func weeklyStrategyTable(w *pgstore.RetroWeekly, zSigma, zLim float64) string {
	var b strings.Builder
	for _, s := range w.Strategies {
		dis := ""
		if s.Disabled {
			dis = " DISABLED(" + s.Reason + ")"
		}
		fmt.Fprintf(&b, "%s trades=%d win_rate=%.2f pnl=$%.4f %s%s\n%s", s.Strategy, s.Trades, s.WinRate, s.TotalPnl, hprText(s), dis, strategyEvidence(s, w.DepSeq[s.Strategy], zSigma, zLim))
	}
	return b.String()
}
