package scan

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/data/fact"
	"nox/internal/store/pgstore"
)

// bookLiveSessionStore anchorDead 闸锚假店：簿有行（HasXAssetMonitorRow=true）
// +trade_session 周表供（BRENT#0=周日 22:05→21:00 UTC 窗实据值 13251260）。
type bookLiveSessionStore struct {
	*fakeMtmPaperStore
	sessionFacts []fact.Fact
}

func (s *bookLiveSessionStore) HasXAssetMonitorRow(context.Context, string, string, string) (bool, error) {
	return true, nil
}
func (s *bookLiveSessionStore) LatestFactsByKind(context.Context, string) ([]fact.Fact, error) {
	return s.sessionFacts, nil
}

// TestAnchorDead_SessionGate entry_basis 锚死会话闸锚：
// 休市窗簿断供=豁免（无警）；开市窗锚死=照报（warn 末行令在案）；
// 会话表缺席=无证据不误赦（照报）。
func TestAnchorDead_SessionGate(t *testing.T) {
	// EntryTs 固定字面量（复审修件 10-19）：原 time.Now()-48h 与下方固定 sat/sun 参考轴
	// 混用两个时间系——真实时间越过 2026-10-19T11:45Z 后 age 恒 <15min 闸，用例③永假红
	// （干净 master 亲证复现）。固定后 age(sat)=36h/age(sun)=71h，三支路语义不变、永久确定。
	pos := pgstore.PaperSpotCarry{
		VenueA: "3001157", VenueB: "binance", Symbol: "BRENT",
		Structure: "short_cfd_long_perp", EntryTs: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC),
	}
	active := []pgstore.PaperSpotCarry{pos}
	view := newAssetClassView(nil)
	// BRENT#0=周日 22:05→21:00（跨午夜窗）：value=day*1e8+start*1e4+end。
	sf := []fact.Fact{{Kind: fact.KindTradeSession, Venue: "3001157", Symbol: "BRENT#0", Value: 13251260}}

	// ①休市窗（周六 12:00 UTC，不在周日窗）→豁免零警。
	stClosed := &bookLiveSessionStore{fakeMtmPaperStore: &fakeMtmPaperStore{rows: map[int64]*pgstore.PaperSpotCarry{}, ticks: map[string][]pgstore.TickRow{}}, sessionFacts: sf}
	sat, _ := time.Parse(time.RFC3339, "2026-10-17T12:00:00Z")
	warnActiveBookAbsent(context.Background(), stClosed, active, view, sat)
	for _, a := range stClosed.alerts {
		if strings.Contains(a, "锚死") {
			t.Fatalf("休市窗锚死不应报: %q", a)
		}
	}

	// ②开市窗（周日 23:00 UTC，22:05 窗内）→锚死照报。
	stOpen := &bookLiveSessionStore{fakeMtmPaperStore: &fakeMtmPaperStore{rows: map[int64]*pgstore.PaperSpotCarry{}, ticks: map[string][]pgstore.TickRow{}}, sessionFacts: sf}
	sun, _ := time.Parse(time.RFC3339, "2026-10-18T23:00:00Z")
	warnActiveBookAbsent(context.Background(), stOpen, active, view, sun)
	var saw bool
	for _, a := range stOpen.alerts {
		if strings.Contains(a, "锚死") {
			saw = true
		}
	}
	if !saw {
		t.Fatal("开市窗锚死应报")
	}

	// ③会话表缺席→不误赦照报。
	stNone := &bookLiveSessionStore{fakeMtmPaperStore: &fakeMtmPaperStore{rows: map[int64]*pgstore.PaperSpotCarry{}, ticks: map[string][]pgstore.TickRow{}}}
	warnActiveBookAbsent(context.Background(), stNone, active, view, sat)
	saw = false
	for _, a := range stNone.alerts {
		if strings.Contains(a, "锚死") {
			saw = true
		}
	}
	if !saw {
		t.Fatal("会话表缺席锚死应照报")
	}
}
