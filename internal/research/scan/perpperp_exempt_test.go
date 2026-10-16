package scan

// perpperp_exempt_test.go —— perp_perp 结构性 conv-N/A 豁免锚：basis 双 NULL
// 不再被缺账闸永拒（est_fee 仍必验）；accrued≤fee 仍如实拒（豁免≠放行）。

import (
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

func TestPerpPerp_ConvNAExempt(t *testing.T) {
	fee, fa := 0.5, 20.0 // funding_ann 20%（%口径）≥8=引擎健在
	base := pgstore.PaperSpotCarry{
		Status: "active", Structure: "perp_perp", Side: "short_perp",
		EntryTs: time.Now().Add(-6 * 24 * time.Hour), AccruedUSD: 10,
		EstRoundTripFeeUSD: &fee, QtyUSD: 1000, LastFundingAnn: &fa,
		// entry/last basis 双 NULL=结构性（无 spot 腿永 NULL）。
	}
	if ok, why := GraduationQualifiedReason(base, time.Now()); !ok {
		t.Fatalf("perp_perp conv-N/A 应豁免过缺账闸: %s", why)
	}
	// est_fee 缺仍拒（豁免不收窄其它字段闸）。
	nofee := base
	nofee.EstRoundTripFeeUSD = nil
	if ok, _ := GraduationQualifiedReason(nofee, time.Now()); ok {
		t.Fatal("est_fee NULL 应仍拒")
	}
	// accrued 盖不住 fee → 净额非正仍拒（豁免≠NULL 当 0 放行）。
	poor := base
	poor.AccruedUSD = 0.1
	if ok, _ := GraduationQualifiedReason(poor, time.Now()); ok {
		t.Fatal("accrued<fee 应仍拒")
	}
	// 非豁免结构（cfd）basis 缺仍拒——分型闸不误放。
	cfd := base
	cfd.Structure = "long_cfd_short_perp"
	if ok, _ := GraduationQualifiedReason(cfd, time.Now()); ok {
		t.Fatal("cfd 结构 basis 缺应仍拒")
	}
}
