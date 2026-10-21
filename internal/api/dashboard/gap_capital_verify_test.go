package dashboard

import (
	"context"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

// gapVerifyFake D-652-C 核验桩（嵌入 fakeGradDB 覆写四面——接口完备性零费）。
type gapVerifyFake struct {
	fakeGradDB
	gaps      []pgstore.ConditionGap
	snaps     []pgstore.CapitalPoolRow
	fulfilled []int64
	alerts    []string
}

func (f *gapVerifyFake) ListMarkedCapitalGaps(context.Context) ([]pgstore.ConditionGap, error) {
	return f.gaps, nil
}
func (f *gapVerifyFake) CapitalPoolSnapshots(context.Context) ([]pgstore.CapitalPoolRow, error) {
	return f.snaps, nil
}
func (f *gapVerifyFake) FulfillConditionGap(_ context.Context, id int64, _ string) error {
	f.fulfilled = append(f.fulfilled, id)
	return nil
}
func (f *gapVerifyFake) InsertAlertDedup(_ context.Context, _, msg string, _ time.Duration) error {
	f.alerts = append(f.alerts, msg)
	return nil
}

// TestGapCapitalVerify 核验环三态锚：
// ①base+target×0.95 达标→fulfilled；②未达标+超 24h→悬空 warn；
// ③基线缺位→绝对额判据。
func TestGapCapitalVerify(t *testing.T) {
	old := time.Now().Add(-25 * time.Hour)
	fresh := time.Now()
	tgt, tgt2, tgt3 := 1500.0, 2000.0, 100.0
	f := &gapVerifyFake{
		gaps: []pgstore.ConditionGap{
			{ID: 1, Kind: "capital", TargetBucket: "okx:unified:USDT",
				TargetUSD: &tgt, MarkedAt: &fresh, ResolveNote: "投资人页回执 mark_base_usd=1000"},
			{ID: 2, Kind: "capital", TargetBucket: "okx:unified:USDT",
				TargetUSD: &tgt2, MarkedAt: &old, ResolveNote: "投资人页回执 mark_base_usd=1000"},
			{ID: 3, Kind: "capital", TargetBucket: "gate:spot:USDT",
				TargetUSD: &tgt3, MarkedAt: &old, ResolveNote: "投资人页回执"},
		},
		snaps: []pgstore.CapitalPoolRow{
			{Venue: "okx", VenueType: "unified", Currency: "USDT", Equity: 2600}, // ①达 2425
			{Venue: "gate", VenueType: "spot", Currency: "USDT", Equity: 97},     // ③达 95 绝对额
		},
	}
	m := &IdleCapitalMonitor{svc: &Service{gradStore: f}}
	m.verifyMarkedCapital(context.Background())
	if len(f.fulfilled) != 2 || f.fulfilled[0] != 1 || f.fulfilled[1] != 3 {
		t.Fatalf("fulfilled 集=%v（应 [1,3]）", f.fulfilled)
	}
	if len(f.alerts) != 1 {
		t.Fatalf("悬空告警应 1 条（gap#2），实=%v", f.alerts)
	}
}
