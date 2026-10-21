// carry_regime_absent_test.go —— STATE 行50 锚：funding_absent 在册如实拦
// （非 worst 撞拒）+ 原播0连拍自动标落账。
package exec

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"nox/internal/store/pgstore"
)

type fakeAbsentQ struct {
	absent  bool
	rows    []pgstore.FactPoint
	marks   []string
	readErr error
}

func (f *fakeAbsentQ) FundingPredWindow(context.Context, string, string, time.Time) ([]pgstore.FactPoint, error) {
	return f.rows, nil
}
func (f *fakeAbsentQ) IsFundingAbsent(context.Context, string, string) (bool, error) {
	return f.absent, f.readErr
}
func (f *fakeAbsentQ) UpsertCapabilityConstraint(_ context.Context, c *pgstore.CapabilityConstraint) error {
	f.marks = append(f.marks, c.Subject)
	return nil
}

func absentCfg() CarryRegimeConfig {
	return CarryRegimeConfig{Window: time.Hour, FloorPct: 2.0, MinSamples: 1}
}

func TestFundingAbsent_UpstreamReject(t *testing.T) {
	q := &fakeAbsentQ{absent: true}
	ok, reason := CarryFundingRegimeCheck(context.Background(), q, "okx:spot", "okx:perp", "JP225USDT", "", time.Now(), absentCfg())
	if ok || !strings.Contains(reason, "funding_absent 在册") {
		t.Fatalf("在册应如实拦: ok=%v reason=%s", ok, reason)
	}
}

func TestFundingAbsent_AutoMark(t *testing.T) {
	absentMarkMu.Lock()
	absentMarkMu.m = map[string]int{}
	absentMarkMu.Unlock()
	// JP225 实录形：窗内含 venue 原播 0 + latest>0 → worst=0<floor 永拒；连拍3→自动标。
	rows := []pgstore.FactPoint{{Value: 0.5}, {Value: 0}, {Value: 0.6}}
	q := &fakeAbsentQ{rows: rows}
	for i := 0; i < 3; i++ {
		ok, _ := CarryFundingRegimeCheck(context.Background(), q, "okx:spot", "okx:perp", "JP225USDT", "", time.Now(), absentCfg())
		if ok {
			t.Fatalf("worst=0 应拒")
		}
	}
	want := pgstore.FundingAbsentSubject("JP225USDT", "okx")
	for _, m := range q.marks {
		if m == want {
			return
		}
	}
	t.Fatalf("连拍3应自动标 %s, marks=%v", want, q.marks)
}

func TestFundingAbsent_NoNativeZeroNoMark(t *testing.T) {
	absentMarkMu.Lock()
	absentMarkMu.m = map[string]int{}
	absentMarkMu.Unlock()
	q := &fakeAbsentQ{rows: []pgstore.FactPoint{{Value: -1}, {Value: 0.5}}}
	for i := 0; i < 4; i++ {
		CarryFundingRegimeCheck(context.Background(), q, "okx:spot", "okx:perp", "X", "", time.Now(), absentCfg())
	}
	if len(q.marks) != 0 {
		t.Fatalf("无原播0不得自动标: %v", q.marks)
	}
}

var _ = fmt.Sprint
