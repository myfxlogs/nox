// dci_tail_test.go —— D-526 锚：σ 驱动界值域+尾损盖溢价拒单对拍。
package gate

import (
	"math"
	"testing"

	"nox/internal/store/pgstore"
)

func TestSigmaTailGate(t *testing.T) {
	f64 := func(v float64) *float64 { return &v }
	cons := []pgstore.CapabilityConstraint{
		{Subject: "dci_put_otm_k", ValueNum: f64(1.5)},
		{Subject: "dci_tenor_max_days", ValueNum: f64(30)},
	}
	k, tmax, ok := SigmaTailParams(cons, "PUT")
	if !ok || k != 1.5 || tmax != 30 {
		t.Fatalf("params: %v %v %v", k, tmax, ok)
	}
	// SG 验收口径：σ=3.5%/tenor=16d/spot=81800 → 界率 1−1.5×0.035×4=0.79。
	r := SigmaBoundRatio("PUT", 0.035, 1.5, 16)
	if r < 0.78 || r > 0.84 {
		t.Fatalf("PUT σ 界率=%.4f 不在 0.79-0.83 域", r)
	}
	if rc := SigmaBoundRatio("CALL", 0.035, 1.5, 16); rc < 1.16 || rc > 1.22 {
		t.Fatalf("CALL σ 界率=%.4f", rc)
	}
	// 约束行缺 → fail-closed。
	if _, _, ok := SigmaTailParams(nil, "PUT"); ok {
		t.Fatal("约束缺应 fail-closed")
	}
	// 尾损对拍：浅虚值 PUT（strike=77500=5.3%OTM, σ=3.5%, 16d）BS put/spot 年化
	// 应显著>0.65%/16d 权利金（≈14.8% ann）——net<0 拒档语义钉死。
	tail := TailCostAnn("PUT", 81800, 77500, 0.035, 16)
	prem := 0.0065 / 16 * 365
	if prem-tail >= 0 {
		t.Fatalf("浅档应尾损盖溢价: prem=%.4f tail=%.4f", prem, tail)
	}
	// 深虚值档尾损衰减：10% OTM tail 应 << 浅档。
	deep := TailCostAnn("PUT", 81800, 73620, 0.035, 16)
	if !(deep < tail) || math.IsInf(deep, 0) {
		t.Fatalf("深档尾损未衰减: %v vs %v", deep, tail)
	}
	if v := TailCostAnn("PUT", 0, 77500, 0.035, 16); !math.IsInf(v, 1) {
		t.Fatal("参数缺应 +Inf fail-closed")
	}
}

// TestTailCostAnn_CallSign 尾损数学核正锚（covered_call tail=-4492% 病灶）：
// CALL 式原误书 −K·N(−d2)（应 −K·N(d2)）——深虚值 N(−d2)≈1 →
// price≈S·N(d1)−K 大额负值=符号翻负+量级爆。95000C@65d 重算须正且合理。
func TestTailCostAnn_CallSign(t *testing.T) {
	// 95000C@65d（sell_high 验收锚形：spot=81800 σ=3.5%）——BS call≈4669 →
	// tail≈4669/81800×365/65≈0.32（32% ann）；旧错号得 −293% 量级负值。
	tail := TailCostAnn("CALL", 81800, 95000, 0.035, 65)
	if tail <= 0 {
		t.Fatalf("CALL 尾损应正: %.4f（错号回归）", tail)
	}
	if tail < 0.15 || tail > 0.60 {
		t.Fatalf("CALL 尾损量级异常: %.4f（期望 ~0.32）", tail)
	}
	// 深虚值衰减：strike 更高 tail 更小（同 PUT 侧惯例）。
	deep := TailCostAnn("CALL", 81800, 110000, 0.035, 65)
	if !(deep < tail) {
		t.Fatalf("深档 CALL 尾损未衰减: %v vs %v", deep, tail)
	}
	// 非负不变量：OTM CALL 尾损永非负（卖方条件损失）。
	for _, k := range []float64{95000, 100000, 120000, 150000} {
		if v := TailCostAnn("CALL", 81800, k, 0.035, 65); v < 0 {
			t.Fatalf("OTM CALL tail 负值: strike=%.0f tail=%.4f", k, v)
		}
	}
}
