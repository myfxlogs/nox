package reconcile

import "testing"

// TestIsARBOrder 覆盖 D-032 系统外单判别契约：
// ARB 源订单 = exec.OrderComment("arb") 前缀；其余（遗留 ca_/cx_/tri_ 格式、手工、空）一律系统外单。
func TestIsARBOrder(t *testing.T) {
	cases := []struct {
		name    string
		comment string
		want    bool
	}{
		{"当前 ARB 单", "arb", true},
		{"当前 ARB 单（带后缀）", "arb-leg1", true},
		{"当前 ARB 单（带数字）", "arb-12345", true},
		{"遗留 carry 单", "ca_63ca13306295e101-leg1", false},
		{"遗留 cross-exchange 单", "cx_681e2b662bafee88-leg0", false},
		{"遗留三角单", "tri_abc123def456-leg2", false},
		{"手工单（UUID）", "1f9c3a1e-8f2b-4c6d-9a0e-5b8f7c2d4a6b", false},
		{"空 comment", "", false},
		{"broker 默认 comment", "Buy EURUSD", false},
	}
	for _, c := range cases {
		if got := isARBOrder(c.comment); got != c.want {
			t.Errorf("%s: isARBOrder(%q) = %v, want %v", c.name, c.comment, got, c.want)
		}
	}
}
