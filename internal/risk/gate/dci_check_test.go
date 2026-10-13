package gate

import (
	"strings"
	"testing"
)

// TestM43_CheckDCI D-262 M4.3② 命名闸锚：提案态/评估卡/执行轨/休眠轨四检面
// 归单函数；序定——态机先于卡先于模板先于休眠。
func TestM43_CheckDCI(t *testing.T) {
	base := DCIProposalView{ID: 7, TemplateID: "dci_sell_high_covered_call", Venue: "binance", Status: "proposed", HasCard: true, Verdict: "viable"}

	if got := CheckDCI(base); got != "" {
		t.Fatalf("全过应空串: %s", got)
	}
	p := base
	p.Status = "expired"
	if got := CheckDCI(p); !strings.Contains(got, "已 expired") {
		t.Fatalf("态机拒: %s", got)
	}
	p = base
	p.HasCard = false
	if got := CheckDCI(p); !strings.Contains(got, "无评估卡") {
		t.Fatalf("无卡拒: %s", got)
	}
	p = base
	p.Verdict = "unevaluable"
	if got := CheckDCI(p); !strings.Contains(got, "评估卡=unevaluable") {
		t.Fatalf("非viable拒: %s", got)
	}
	p = base
	p.TemplateID = "dci_unknown"
	if got := CheckDCI(p); !strings.Contains(got, "未接执行轨") {
		t.Fatalf("模板拒: %s", got)
	}
	p = base
	p.Dormant = true
	if got := CheckDCI(p); !strings.Contains(got, "休眠轨") || !strings.Contains(got, "dci_sell_high_covered_call@binance") {
		t.Fatalf("休眠拒: %s", got)
	}
	// 序锚：态机优于卡——expired+无卡先报态机。
	p = base
	p.Status = "declined"
	p.HasCard = false
	if got := CheckDCI(p); !strings.Contains(got, "已 declined") {
		t.Fatalf("检序错位: %s", got)
	}
}

// TestM43_FailClosedBudget M4.3④ 预留闸锚：默认桩恒拒（M4.4 挂真实现前安全侧）。
func TestM43_FailClosedBudget(t *testing.T) {
	allow, reason := FailClosedBudget{}.CheckBudget(t.Context(), "SWAPARB", 100)
	if allow || reason == "" {
		t.Fatalf("默认桩应恒拒: allow=%v reason=%q", allow, reason)
	}
}
