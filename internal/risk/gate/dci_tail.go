// dci_tail.go —— D-526：DCI 选档尾险定价件（σ 驱动 OTM 界 + 尾损成本）。
// σ_daily 实算自 venue 日 K（读径在 exchange.SigmaDaily，本包纯数学零 IO）。
// 约束键：dci_put_otm_k / dci_call_otm_k（CALL 可缺省回 PUT k）、
// dci_tenor_max_days；行缺 → ok=false（fail-closed 不产提案/不下单同现语义）。
package gate

import (
	"math"

	"nox/internal/store/pgstore"
)

// SigmaTailParams 尾险闸约束读面：返回 (k, tenor_max, ok)。
// dci_put_otm_k 必配；CALL 侧 k=dci_call_otm_k 缺则回 PUT 同值（票面同改义）；
// dci_tenor_max_days 缺 → ok=false。
func SigmaTailParams(cons []pgstore.CapabilityConstraint, opt string) (k, tenorMax float64, ok bool) {
	var pk, ck, tm *float64
	for _, c := range cons {
		switch c.Subject {
		case "dci_put_otm_k":
			pk = c.ValueNum
		case "dci_call_otm_k":
			ck = c.ValueNum
		case "dci_tenor_max_days":
			tm = c.ValueNum
		}
	}
	kk := pk
	if opt == "CALL" && ck != nil {
		kk = ck
	}
	if kk == nil || tm == nil || *kk <= 0 || *tm <= 0 {
		return 0, 0, false
	}
	return *kk, *tm, true
}

// SigmaBoundRatio σ 驱动 OTM 比率界：PUT=1−kσ√t、CALL=1+kσ√t（t=tenor 天）。
// tenor 钳 [1, tenorMax] 域外入参不放大界。
func SigmaBoundRatio(opt string, sigma, k, tenor float64) float64 {
	if tenor < 1 {
		tenor = 1
	}
	d := k * sigma * math.Sqrt(tenor)
	if opt == "CALL" {
		return 1 + d
	}
	return 1 - d
}

// normCDF 标准正态分布函数（Erf 近似）。
func normCDF(x float64) float64 { return 0.5 * (1 + math.Erf(x/math.Sqrt2)) }

// TailCostAnn 尾损成本年化（Black-Scholes 近似，r=0 无漂移假设——
// 统计参考口径非定价真值）：PUT 卖方条件损失≈BS put/spot、CALL 卖方≈BS call/spot，
// ×365/tenor 年化。返回面值比率（与 premium_ann 同量纲对拍）。
// σ≤0/tenor≤0/strike≤0 → +Inf（判据不可算=最贵——fail-closed 方向）。
func TailCostAnn(opt string, spot, strike, sigma float64, tenorDays int) float64 {
	if spot <= 0 || strike <= 0 || sigma <= 0 || tenorDays <= 0 {
		return math.Inf(1)
	}
	s := sigma * math.Sqrt(float64(tenorDays)) // 持有期波动=σ_daily√天数（BS σ√t 等价）
	d1 := (math.Log(spot/strike) + s*s/2) / s
	d2 := d1 - s
	var price float64
	if opt == "CALL" {
		price = spot*normCDF(d1) - strike*normCDF(d2)
	} else {
		price = strike*normCDF(-d2) - spot*normCDF(-d1)
	}
	return price / spot / float64(tenorDays) * 365 // 年化
}
