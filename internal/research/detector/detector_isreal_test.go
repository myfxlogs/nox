package detector

import "testing"

// TestIsRealPair D-178：检测层 is_real 配对检查（fail-open 语义）。
func TestIsRealPair(t *testing.T) {
	tr := true
	fa := false
	tests := []struct {
		name    string
		realMap map[string]*bool
		venueA  string
		venueB  string
		want    bool
	}{
		{"nil map → 放行", nil, "a", "b", true},
		{"空 map → 放行", map[string]*bool{}, "a", "b", true},
		{"venue 缺失 → 放行", map[string]*bool{"a": &tr}, "a", "b", true},
		{"nil 值 → 放行", map[string]*bool{"a": &tr, "b": nil}, "a", "b", true},
		{"两腿 true → 配对", map[string]*bool{"a": &tr, "b": &tr}, "a", "b", true},
		{"两腿 false → 配对", map[string]*bool{"a": &fa, "b": &fa}, "a", "b", true},
		{"一 true 一 false → 拒绝", map[string]*bool{"a": &tr, "b": &fa}, "a", "b", false},
		{"对称：a=false b=true → 拒绝", map[string]*bool{"a": &fa, "b": &tr}, "a", "b", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isRealPair(tt.realMap, tt.venueA, tt.venueB); got != tt.want {
				t.Fatalf("isRealPair(%v, %q, %q) = %v, want %v", tt.realMap, tt.venueA, tt.venueB, got, tt.want)
			}
		})
	}
}
