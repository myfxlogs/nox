package mmm

import (
	"math"
	"strings"
	"testing"
)

// TestD180_BinomialBound 公式对拍：n=100 p=.51 Z=3 → L≈.3593（票面钦定锚）。
func TestD180_BinomialBound(t *testing.T) {
	l := BinomialLowerBound(0.51, 100, 3)
	if math.Abs(l-0.3593) > 0.001 {
		t.Fatalf("B.12 下界错: got %.4f want ≈0.3593", l)
	}
	// 反解 N 量级：p=.51 在 3σ 下确胜率>50% 需 ~2.2 万笔（非硬编码常数对拍）。
	n := BinomialNNeeded(0.51, 0.50, 3)
	if n < 20000 || n > 30000 {
		t.Fatalf("反解 N 量级错: got %d", n)
	}
	if BinomialNNeeded(0.45, 0.50, 3) != -1 {
		t.Fatal("p≤target 应不可达")
	}
}

// TestD180_TurningPoints 已知序列：完美交替盈亏 8 笔 → 中段全转折点 obs=6。
func TestD180_TurningPoints(t *testing.T) {
	obs, exp, sd := TurningPoints([]float64{1, -1, 1, -1, 1, -1, 1, -1})
	if obs != 6 {
		t.Fatalf("交替序列转折点应=6, got %d", obs)
	}
	if math.Abs(exp-4.0) > 1e-9 || math.Abs(sd-math.Sqrt(99.0/90.0)) > 1e-9 {
		t.Fatalf("E/sd 错: E=%.3f sd=%.4f", exp, sd)
	}
	// 全同号（同类生同类极端）：0 转折点，Z 强负。
	obs, _, sd = TurningPoints([]float64{1, 1, 1, 1, 1, 1, 1, 1})
	if obs != 0 || sd == 0 {
		t.Fatalf("全同号序列: obs=%d sd=%f", obs, sd)
	}
}

// TestD180_ElimEvidence 文案字段：含置信下界与距宣判笔数；低胜率→不可达。
func TestD180_ElimEvidence(t *testing.T) {
	s := ElimEvidence(53, 0.42, 0.30, 2)
	if !strings.Contains(s, "winRate 42% @n=53") || !strings.Contains(s, "置信下界") || !strings.Contains(s, "还需") {
		t.Fatalf("证据文案缺字段: %s", s)
	}
	if s2 := ElimEvidence(53, 0.10, 0.30, 2); !strings.Contains(s2, "不可达") {
		t.Fatalf("低胜率应标不可达: %s", s2)
	}
	if s3 := ElimEvidence(1, 0.5, 0.30, 2); !strings.Contains(s3, "不足") {
		t.Fatalf("n<2 应标不足: %s", s3)
	}
}
