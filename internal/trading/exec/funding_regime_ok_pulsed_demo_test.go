package exec

// D-328 闸 demo 域脉动语义（PulsedAware）——
// T1 脉动窗：6 零+18 热（mean=15%）→ PulsedAware=true 过 / false 拒（红=现码两域同拒）；
// T2 回归锚：稳态双过 / 坍塌窗双拒 / n<minSamples 双拒（real 域语义一字不动的另一面=旧锚不破）。
import (
	"testing"

	"arb/internal/store/pgstore"
)

func d334Rows(vals []float64) []pgstore.FactPoint {
	rows := make([]pgstore.FactPoint, len(vals))
	for i, v := range vals {
		rows[i] = pgstore.FactPoint{Value: v}
	}
	return rows
}

func TestD334c_FundingRegimeOK_PulsedDemo(t *testing.T) {
	// 脉动窗（升序 rows，零簇在前热簇在尾=脉动型真实形态）：6 零+18 热（20%）→
	// mean=15%≥floor=2、latest=20≥2；worst=0。latest 落零会触发既有非收入腿豁免（T1b），
	// 故零簇必须在前——与 XPTUSDT「零簇成簇后转热」生产形态一致。
	pulsed := d334Rows([]float64{0, 0, 0, 0, 0, 0, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20, 20})
	if gate, income, reason := FundingRegimeOK(pulsed, 1, 2.0, 12, true); !gate || !income {
		t.Errorf("pulsed demo: gate=%v income=%v（红=现码 worst 判两域同拒）reason=%s", gate, income, reason)
	}
	if gate, _, reason := FundingRegimeOK(pulsed, 1, 2.0, 12, false); gate {
		t.Errorf("pulsed real: worst=0 < floor 必须拒（real 铁边界），reason=%s", reason)
	}
}

func TestD334c_FundingRegimeOK_RegressionAnchors(t *testing.T) {
	steady := d334Rows([]float64{5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5, 5})
	collapsed := d334Rows([]float64{1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1, 1})
	tooFew := d334Rows([]float64{20, 20, 20, 20, 20})
	for _, pulsed := range []bool{true, false} {
		if gate, income, reason := FundingRegimeOK(steady, 1, 2.0, 12, pulsed); !gate || !income {
			t.Errorf("pulsed=%v steady: gate=%v income=%v reason=%s, want 双过", pulsed, gate, income, reason)
		}
		if gate, _, reason := FundingRegimeOK(collapsed, 1, 2.0, 12, pulsed); gate {
			t.Errorf("pulsed=%v collapsed (mean=1<2): want 双拒, reason=%s", pulsed, reason)
		}
		if gate, _, reason := FundingRegimeOK(tooFew, 1, 2.0, 12, pulsed); gate {
			t.Errorf("pulsed=%v n=5<12: want fail-closed 双拒, reason=%s", pulsed, reason)
		}
	}
	// demo reason 串格式锚（mean=%.4g latest=%.4g n=%d < floor %.2f）
	if _, _, reason := FundingRegimeOK(collapsed, 1, 2.0, 12, true); !contains(reason, "mean=1") || !contains(reason, "latest=1") || !contains(reason, "n=12") {
		t.Errorf("demo reason format want mean/latest/n fields, got %q", reason)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}
