package scan

// fee_cover_gate_test.go —— 息差引擎独立盖费闸锚（net 顶包病灶修：conv 一次
// 性价差不得顶包费下亏引擎；accrued≥|est_fee| 才可毕业）。

import (
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func TestFeeCoverGate(t *testing.T) {
	fee, fa, conv := 7.0, 20.0, 9.0
	eb, lb := 10.0, 0.0 // basis 10bps→0：正收敛≈qty×10bp（dir 视 side）
	base := pgstore.PaperSpotCarry{
		Status: "active", Structure: "long_cfd_short_perp", Side: "short_cfd",
		EntryTs: time.Now().Add(-6 * 24 * time.Hour), AccruedUSD: 1.5,
		EstRoundTripFeeUSD: &fee,
		EntryBasisBps:      &eb, LastBasisBps: &lb, ConvergenceUSD: &conv,
		// qty 90000：conv=dir−1×(0−10bps)×90000/1e4=+9 → net=1.5−7+9=+3.5 顶包净正
		QtyUSD: 90000, LastFundingAnn: &fa,
	}
	// ①accrued<fee+conv 顶包净正 → 拒（net=1.5-7+9=+3.5 正，息差引擎费下亏）
	if ok, why := GraduationQualifiedReason(base, time.Now()); ok {
		t.Fatalf("息差费下亏+conv 顶包应拒: ok=%v", ok)
	} else if want := "息差未盖费"; !strings.Contains(why, want) {
		t.Fatalf("拒因应含%q: %q", want, why)
	}
	// ②accrued≥fee → 过（息差单独回本=结构可自养）
	pass := base
	pass.AccruedUSD = 8
	if ok, why := GraduationQualifiedReason(pass, time.Now()); !ok {
		t.Fatalf("accrued≥fee 应过: %s", why)
	}
	// ③convNA（perp_perp）径不引入新拒因——accrued≥fee 仍照过
	pp := base
	pp.Structure, pp.Side = "perp_perp", "short_perp"
	pp.EntryBasisBps, pp.LastBasisBps, pp.ConvergenceUSD = nil, nil, nil
	pp.AccruedUSD = 8
	if ok, why := GraduationQualifiedReason(pp, time.Now()); !ok {
		t.Fatalf("perp_perp accrued≥fee 应过（豁免域不变）: %s", why)
	}
	// ④exited 径同闸：adverse 退出但息差未盖费=拒
	ex := base
	ex.Status = "exited"
	ex.ExitReason = "adverse"
	ets := time.Now().Add(-2 * 24 * time.Hour)
	ex.ExitTs = &ets
	ex.EntryTs = ets.Add(-6 * 24 * time.Hour)
	ex.AccruedUSD = 1.5
	if ok, why := GraduationQualifiedReason(ex, time.Now()); ok {
		t.Fatalf("exited 息差费下亏应拒: ok=%v", ok)
	} else if !strings.Contains(why, "息差未盖费") {
		t.Fatalf("exited 拒因应含息差未盖费: %q", why)
	}
}

func TestFeeCoverGate_NegFee(t *testing.T) {
	fee, fa, conv := -7.0, 20.0, 9.0
	eb, lb := 10.0, 0.0
	p := pgstore.PaperSpotCarry{
		Status: "active", Structure: "long_cfd_short_perp", Side: "short_cfd",
		EntryTs: time.Now().Add(-6 * 24 * time.Hour), AccruedUSD: 1.5,
		EstRoundTripFeeUSD: &fee,
		EntryBasisBps:      &eb, LastBasisBps: &lb, ConvergenceUSD: &conv,
		QtyUSD: 90000, LastFundingAnn: &fa,
	}
	ok, why := GraduationQualifiedReason(p, time.Now())
	if ok {
		t.Fatal("neg-fee should reject")
	} else if !strings.Contains(why, "未盖费") {
		t.Fatalf("reason=%q", why)
	}
}
