// carry_regime.go —— FundingRegimeOK 自 internal/trading/exec 迁入（D-651-c 闸收口账
// 归编路线：判据纯函数族收 risk 单表面——本函数纯判据无 DB/IO 三返 gate/income/
// reason，risk 根包叶件零回引。判定语义一字不改）。
package risk

import (
	"fmt"

	"nox/internal/store/pgstore"
)

// FundingRegimeOK 单腿持久性判定（纯函数，可单测；rows 按 ts 升序）。
// 返回 gate=false=该腿拒绝；income=false=非收入腿（调用方跳过不约束）。
//   - 样本不足（含零样本）→ (false,false,"insufficient n ...")——未知 regime 不放行
//   - requiredSign×latest ≤ 0 → (true,false,"non-income leg skip ...")——当前付息/零息腿豁免
//   - 收入腿：窗口内全部样本 requiredSign×v ≥ floorPct（sign>0 看 minV、sign<0 看 maxV≤−floor）
//
// reason 串带 latest/min/max/n 供日志与 FAILED message。
func FundingRegimeOK(rows []pgstore.FactPoint, requiredSign, floorPct float64, minSamples int, pulsedAware bool) (gate bool, income bool, reason string) {
	n := len(rows)
	if n == 0 || n < minSamples {
		return false, false, fmt.Sprintf("insufficient n=%d < %d", n, minSamples)
	}
	latest := rows[n-1].Value
	if requiredSign*latest <= 0 {
		return true, false, fmt.Sprintf("non-income leg skip latest=%.4g", latest)
	}
	// demo 域脉动型分支（PulsedAware）——mean≥floor && latest≥floor && n≥minSamples
	//（n 检查已在函数头）。经济语义：均值含零样本=真实期望收益（零期拉低均值是诚实反映非一票否决）；
	// latest=当下未坍塌；fail-closed 保留。已知取舍（显式接受）：窗内中段塌落（latest 尚正、均值
	// 尚高）逃逸比 worst 判多——demo=学习环境；real 域不受影响（一字不动=铁边界）。
	if pulsedAware {
		var sum float64
		for _, r := range rows {
			sum += requiredSign * r.Value
		}
		mean := sum / float64(n)
		signedLatest := requiredSign * latest
		if mean < floorPct || signedLatest < floorPct {
			return false, true, fmt.Sprintf("mean=%.4g latest=%.4g n=%d < floor %.2f", mean, signedLatest, n, floorPct)
		}
		return true, true, fmt.Sprintf("income ok (demo pulsed) mean=%.4g latest=%.4g n=%d", mean, signedLatest, n)
	}
	minV, maxV := rows[0].Value, rows[0].Value
	for _, r := range rows[1:] {
		if r.Value < minV {
			minV = r.Value
		}
		if r.Value > maxV {
			maxV = r.Value
		}
	}
	worst := minV // requiredSign×v 的窗口最小值
	if requiredSign < 0 {
		worst = -maxV
	}
	if worst < floorPct {
		return false, true, fmt.Sprintf("latest=%.4g min=%.4g max=%.4g worst=%.4g n=%d < floor %.2f", latest, minV, maxV, worst, n, floorPct)
	}
	return true, true, fmt.Sprintf("income ok latest=%.4g min=%.4g max=%.4g n=%d", latest, minV, maxV, n)
}
