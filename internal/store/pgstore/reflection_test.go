package pgstore

import (
	"strings"
	"testing"
	"time"
)

// 自省模板四段锚（票面：seed 退出→reflection 模板四段断言）。
func TestPaperReflection_FourSegments(t *testing.T) {
	open := time.Date(2026, 10, 18, 0, 0, 0, 0, time.UTC)
	exit := open.Add(24 * time.Hour)
	fund, basis, fee, acc, conv, qty := 10.0, 2.5, 0.5, 0.8, -0.2, 5000.0
	txt := paperReflection(&paperReflectRow{
		Side: "short_perp", Structure: "spot_perp", ExitReason: "adverse",
		EntryFundingAnn: &fund, EntryBasisBps: &basis, EstRoundTripFeeUSD: &fee,
		AccruedUSD: &acc, ConvergenceUSD: &conv, QtyUSD: &qty,
		EntryTs: &open, ExitTs: &exit,
	})
	for _, seg := range []string{"入场假设:", "成本差:", "出场:", "模型偏差:"} {
		if !strings.Contains(txt, seg) {
			t.Fatalf("模板缺段 %s: %s", seg, txt)
		}
	}
	if !strings.Contains(txt, "出场: adverse") {
		t.Fatalf("exit_reason 未归因: %s", txt)
	}
	// 预期 carry=10%/365×1d×5000=1.3699 vs 实结=0.8-0.2=0.6——落库值直读非重建断言。
	if !strings.Contains(txt, "实结+0.6000USD") {
		t.Fatalf("实结值非落库直读: %s", txt)
	}
}

// 缺锚如实缺（不占位虚报——预期 NULL→段缺）。
func TestPaperReflection_MissingAnchors(t *testing.T) {
	txt := paperReflection(&paperReflectRow{Side: "x", ExitReason: "venue_starved"})
	if !strings.Contains(txt, "缺") || !strings.Contains(txt, "venue_starved") {
		t.Fatalf("缺锚未如实缺: %s", txt)
	}
}
