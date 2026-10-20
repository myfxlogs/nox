// d476a_evidence_test.go D-476a：档案证据级别徽标验收锚（计算同点产出+降级表）。
package dashboard

import (
	"context"
	"nox/internal/data/bus"
	"testing"
	"time"

	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
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
	s.qbus = bus.New() // D-633 返修：判价现态源注假帧（gate4 判面）
	s.qbus.Publish(bus.Quote{Symbol: "XAUUSDm", Venue: "3001157", VenueType: "mt5", Bid: 2400, Ask: 2400.5, Time: time.Now()})
	s.qbus.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "binance", VenueType: "perp", Bid: 2400, Ask: 2400.5, Time: time.Now()})
	// D-565 S2：venue 杠杆实读缝（夹具钉 5x=原口径）。
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 5, nil }
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
		"recommended_leverage":        B, // D-565 S2：venue 实读杠杆（夹具 5x）权威源
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

// TestD476a_CapitalMissingIMR_GradeC：D-006 S1b——imr 缺→capital 按悲观档 20x 估
// （50+200=250 非 NULL），估降级 C（实测才 B）。
func TestD476a_CapitalMissingIMR_GradeC(t *testing.T) {
	f := d471Fixture()
	delete(f.facts, "3001157|")
	rep := d476aSvc(f).gradDossier(context.Background(), f.grad, f.paper)
	if rep.CapitalUsd == nil || *rep.CapitalUsd != 250 {
		t.Fatalf("imr 缺→capital 应按悲观档估 250, got %v", rep.CapitalUsd)
	}
	if g := egOf(rep, "capital_usd"); g != v1.EvidenceGrade_EVIDENCE_GRADE_C {
		t.Errorf("capital grade=%v want C（估）", g)
	}
}

// TestD476a_LiqBufferUpgrade_B：CEX 腿账户实采 buffer_multiple→liq 升 B。
func TestD476a_LiqBufferUpgrade_B(t *testing.T) {
	f := d471Fixture()
	buf := 2.4
	s := d476aSvc(f)
	seedLiveAccounts(t, s, []pgstore.VenueAccountLatest{
		{Venue: "binance", VenueType: "perp", BufferMultiple: &buf},
		{Venue: "gate", VenueType: "perp"}, // 无 buffer 无关行
	})
	rep := s.gradDossier(context.Background(), f.grad, f.paper)
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
