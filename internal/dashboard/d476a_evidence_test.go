// d476a_evidence_test.go D-476a：档案证据级别徽标验收锚（计算同点产出+降级表）。
package dashboard

import (
	"context"
	"testing"
	"time"

	"arb/internal/exec"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

func egOf(rep *v1.GraduationRiskReport, field string) v1.EvidenceGrade {
	for _, g := range rep.FieldGrades {
		if g.Field == field {
			return g.Grade
		}
	}
	return v1.EvidenceGrade_EVIDENCE_GRADE_UNSPECIFIED
}

func d476aSvc(f *fakeGradDB) *Service {
	s := &Service{gradStore: f}
	s.SetCarryRegime(exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12})
	return s
}

// TestD476a_Grades_Baseline：齐全档案各字段级别钉死。
func TestD476a_Grades_Baseline(t *testing.T) {
	f := d471Fixture()
	rep := d476aSvc(f).gradDossier(context.Background(), f.grad, f.paper)
	B, C, D := v1.EvidenceGrade_EVIDENCE_GRADE_B, v1.EvidenceGrade_EVIDENCE_GRADE_C, v1.EvidenceGrade_EVIDENCE_GRADE_D
	cases := map[string]v1.EvidenceGrade{
		"notional_usd":                B,
		"legs":                        B,
		"capital_usd":                 B,
		"recommended_leverage":        C, // venue 档未采集回落 5+note
		"liq_distance_pct_est":        C, // 无 buffer_multiple 快照→公式估算
		"funding_flip_daily_usd":      D, // fixture 无 funding 样本→缺
		"adverse_basis_p95_usd":       B,
		"roundtrip_fee_usd":           B,
		"min_executable:3001157:mt5":  D, // lot filter 未采腿
		"min_executable:binance:perp": D,
	}
	for field, want := range cases {
		if got := egOf(rep, field); got != want {
			t.Errorf("%s grade=%v want %v", field, got, want)
		}
	}
}

// TestD476a_CapitalMissingIMR_GradeD：imr 缺→capital NULL+级 D。
func TestD476a_CapitalMissingIMR_GradeD(t *testing.T) {
	f := d471Fixture()
	delete(f.facts, "3001157|")
	rep := d476aSvc(f).gradDossier(context.Background(), f.grad, f.paper)
	if rep.CapitalUsd != nil {
		t.Fatal("imr 缺→capital 应 NULL")
	}
	if g := egOf(rep, "capital_usd"); g != v1.EvidenceGrade_EVIDENCE_GRADE_D {
		t.Errorf("capital grade=%v want D", g)
	}
}

// TestD476a_LiqBufferUpgrade_B：CEX 腿账户实采 buffer_multiple→liq 升 B。
func TestD476a_LiqBufferUpgrade_B(t *testing.T) {
	f := d471Fixture()
	buf := 2.4
	f.snaps = []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "perp", BufferMultiple: &buf},
		{Venue: "gate", VenueType: "perp"}, // 无 buffer 无关行
	}
	rep := d476aSvc(f).gradDossier(context.Background(), f.grad, f.paper)
	if g := egOf(rep, "liq_distance_pct_est"); g != v1.EvidenceGrade_EVIDENCE_GRADE_B {
		t.Errorf("liq grade=%v want B（buffer_multiple 已采）", g)
	}
}

// TestD476a_FeeMissing_GradeD：est_round_trip_fee 缺→级 D（费率表未盖腿同义）。
func TestD476a_FeeMissing_GradeD(t *testing.T) {
	f := d471Fixture()
	f.paper.EstRoundTripFeeUSD = nil
	rep := d476aSvc(f).gradDossier(context.Background(), f.grad, f.paper)
	if rep.RoundtripFeeUsd != nil {
		t.Fatal("fee 缺→NULL")
	}
	if g := egOf(rep, "roundtrip_fee_usd"); g != v1.EvidenceGrade_EVIDENCE_GRADE_D {
		t.Errorf("fee grade=%v want D", g)
	}
}
