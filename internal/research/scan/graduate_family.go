// graduate_family.go —— D-637 序③：纸面毕业判据升级·族级双轴。
// 费后净=Σnet/N>0（既有单行闸之上补族均值得正——负边族如实判退）；
// 置信界=Vince B.12 二项胜率下界 L=P−Z·√(P(1−P)/(N−1))（L≤0=证据不足非毕业；
// 阈值 app_config：grad_family_min_n 缺省 5 笔 / grad_family_z 缺省 1.645≈90%）。
// 判据叠加非替代——既有单行闸（缺账/全账净/息差盖费/存活率）零放宽。
package scan

import (
	"context"
	"math"
	"strconv"

	"nox/internal/store/pgstore"
)

// FamilyGradStats 族级样本统计（同 Structure 组——N/胜率/净期望/置信下界）。
type FamilyGradStats struct {
	N          int     // 样本数（已结费后净算得行）
	Wins       int     // net>0 行数
	WinRate    float64 // Wins/N
	MeanNetUSD float64 // 费后净均值（含收敛分——全账净口径同单行闸）
	LowerBound float64 // B.12 胜率置信下界（Zσ）
}

// FamilyGradGate 族级双轴判定——minN/Z 由 app_config 可调（行缺保缺省）。
// 返回 (qualified, lb, reason)；lb=判点实算置信下界同源出闸（持久化取此值——
// 禁两处重算漂移；拒径 lb 如实返算出值或 0，语义=未达闸）。
func FamilyGradGate(ctx context.Context, st interface {
	AppConfigInt64(context.Context, string) (int64, bool, error)
	AppConfigFloat(context.Context, string) (float64, bool, error)
}, stats FamilyGradStats) (bool, float64, string) {
	minN := int64(5)
	if v, ok, _ := st.AppConfigInt64(ctx, "grad_family_min_n"); ok && v > 0 {
		minN = v
	}
	z := 1.645
	if v, ok, _ := st.AppConfigFloat(ctx, "grad_family_z"); ok && v > 0 {
		z = v
	}
	lb := stats.WinRate // N<=1 界=minN 闸管样本界（操作阈语义）——lb 如实返观测率非 NaN
	if stats.N > 1 {
		lb = stats.WinRate - z*math.Sqrt(stats.WinRate*(1-stats.WinRate)/float64(stats.N-1))
	}
	if stats.N < int(minN) {
		return false, lb, "族样本不足（N=" + strconv.Itoa(stats.N) + "<" + strconv.FormatInt(minN, 10) + "）"
	}
	if stats.MeanNetUSD <= 0 {
		return false, lb, "族费后净均值非正"
	}
	if lb <= 0 {
		return false, lb, "置信下界≤0（证据不足）"
	}
	return true, lb, ""
}

// familyGradStatsOf rows 内同 Structure 聚合（费后净可算行——NULL 行不入样本）。
func familyGradStatsOf(rows []pgstore.PaperSpotCarry, structure string) FamilyGradStats {
	var st FamilyGradStats
	for _, p := range rows {
		if p.Structure != structure {
			continue
		}
		conv := effectiveConvergenceUSD(p)
		if p.EstRoundTripFeeUSD == nil || conv == nil {
			continue
		}
		net := netAllInUSD(p.AccruedUSD, p.EstRoundTripFeeUSD, conv)
		st.N++
		st.MeanNetUSD += net
		if net > 0 {
			st.Wins++
		}
	}
	if st.N > 0 {
		st.MeanNetUSD /= float64(st.N)
		st.WinRate = float64(st.Wins) / float64(st.N)
	}
	return st
}
