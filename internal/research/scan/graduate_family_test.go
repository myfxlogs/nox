package scan

import (
	"context"
	"testing"
)

type famCfgStub struct{}

func (famCfgStub) AppConfigInt64(context.Context, string) (int64, bool, error) {
	return 0, false, nil
}
func (famCfgStub) AppConfigFloat(context.Context, string) (float64, bool, error) {
	return 0, false, nil
}

// TestFamilyGradGate_ThreeShapes 三构型锚：正边小样本拒/负边大样本拒/正边大样本过。
func TestFamilyGradGate_ThreeShapes(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		stats FamilyGradStats
		want  bool
	}{
		{"正边小样本", FamilyGradStats{N: 3, Wins: 3, WinRate: 1.0, MeanNetUSD: 40.0}, false},
		{"负边大样本", FamilyGradStats{N: 30, Wins: 8, WinRate: 0.27, MeanNetUSD: -2.5}, false},
		{"正边大样本", FamilyGradStats{N: 30, Wins: 21, WinRate: 0.7, MeanNetUSD: 12.0}, true},
	}
	for _, c := range cases {
		got, _, reason := FamilyGradGate(ctx, famCfgStub{}, c.stats)
		if got != c.want {
			t.Fatalf("%s: got=%v want=%v（%s）", c.name, got, c.want, reason)
		}
	}
}
