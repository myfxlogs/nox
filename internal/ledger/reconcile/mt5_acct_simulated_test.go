// d448_acct_mode_test.go —— D-448 S1：MT5 AccountSummary.Type → Simulated 映射表驱动测。
// MT5 Type=string（实测 demo→"demo"/real→"real"，2026-09-24 Exness 277842155/3001157 实证）；
// MT4 Type=AccountType 枚举（Real=0/Contest=1/Demo=2）。
package reconcile

import (
	"testing"

	mt5pb "arb/internal/data/adapter/gen/mtapi/mt5"
)

func TestD448_MT5AcctSimulated(t *testing.T) {
	cases := []struct {
		typ  string
		want bool
	}{
		{"demo", true},         // 实测：Exness 277842155 trial 户
		{"DEMO", true},         // 大小写防御
		{"Demo-Contest", true}, // contest 族
		{"contest", true},
		{"real", false}, // 实测：Exness 3001157 真钱户
		{"Real", false},
		{"", false}, // 空串→不臆造 demo
		{"unknown-x", false},
	}
	for _, c := range cases {
		if got := mt5AcctSimulated(c.typ); got != c.want {
			t.Errorf("mt5AcctSimulated(%q)=%v want %v", c.typ, got, c.want)
		}
		info := accountSummaryToInfoMT5("v", "mt5", &mt5pb.AccountSummary{Type: c.typ})
		if info.Simulated != c.want {
			t.Errorf("accountSummaryToInfoMT5(%q).Simulated=%v want %v", c.typ, info.Simulated, c.want)
		}
	}
}
