package scan

import (
	"context"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// TestD555S3_GraduationReuse S3：双CEX（long_spot_short_perp）纸面行走**同一**
// StepCarryGraduation 判据（零新资格规则）——合格行产生建议且 venue_type_a/b
// 透传到 carry_graduations（approve 腿级 resolver 的数据源）。
func TestD555S3_GraduationReuse(t *testing.T) {
	now := time.Now()
	fee, fa := 0.5, 10.0
	eb, lb := 12.0, 10.0 // short_perp dir=−1：−1×(10−12)×1000/1e4=+0.2 收敛捕获
	p := pgstore.PaperSpotCarry{
		ID: 7, VenueA: "gate", VenueB: "binance",
		VenueTypeA: "spot", VenueTypeB: "perp",
		Symbol: "BTC", Structure: "long_spot_short_perp", Side: "short_perp",
		Status: "active", AccruedUSD: 5.0, EntryTs: now.Add(-6 * 24 * time.Hour),
		QtyUSD:             1000,
		EstRoundTripFeeUSD: &fee, EntryBasisBps: &eb, LastBasisBps: &lb,
		LastFundingAnn: &fa, // D-576 引擎存活检：10%（%口径）≥8=引擎健在
	}
	fs := &fakeGradStore{rows: []pgstore.PaperSpotCarry{p}, existing: map[int64]bool{}}
	n, err := StepCarryGraduation(context.Background(), fs, now, nil)
	if err != nil || n != 1 {
		t.Fatalf("newCount=%d err=%v, want 1/nil", n, err)
	}
	if len(fs.inserted) != 1 {
		t.Fatalf("建议行数=%d, want 1", len(fs.inserted))
	}
	g := fs.inserted[0]
	if g.Structure != "long_spot_short_perp" || g.PaperID != 7 {
		t.Errorf("structure=%q paper_id=%d", g.Structure, g.PaperID)
	}
	if g.VenueA != "gate" || g.VenueB != "binance" {
		t.Errorf("venue_a/b=%q/%q", g.VenueA, g.VenueB)
	}
	if g.VenueTypeA != "spot" || g.VenueTypeB != "perp" {
		t.Fatalf("venue_type 透传丢失：a=%q b=%q", g.VenueTypeA, g.VenueTypeB)
	}
	if g.MtVenue != nil {
		t.Errorf("双CEX 行 mt_venue 应为 NULL，got %v", *g.MtVenue)
	}
}

// TestD555S3_CfdRegression 回归钉：cfd 行 venue_type 列为 ”（存量语义）照常毕业。
func TestD555S3_CfdRegression(t *testing.T) {
	fs := &fakeGradStore{rows: []pgstore.PaperSpotCarry{gradPaper(2, "active", 6, 0, 10, 0, "")}, existing: map[int64]bool{}}
	n, err := StepCarryGraduation(context.Background(), fs, time.Now(), nil)
	if err != nil || n != 1 {
		t.Fatalf("newCount=%d err=%v", n, err)
	}
	if fs.inserted[0].VenueTypeA != "" || fs.inserted[0].VenueTypeB != "" {
		t.Errorf("cfd 行 venue_type 应透传空串，got %q/%q", fs.inserted[0].VenueTypeA, fs.inserted[0].VenueTypeB)
	}
}
