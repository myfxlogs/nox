// d565_verdict_test.go —— D-565 锚测：T2 allocRisk 杠杆实况不可得三态 /
// T3 comboPoolVerdict 快照龄闸 / T6 marginFloor 跳腿计数透出。
package dashboard

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
	"arb/internal/testutil/storetest"
)

// TestD565_AllocRisk_LeverageUnknown T2：venue 杠杆读败 → risk=unknown +
// 「杠杆实况不可得」理由透出；suggested 不反推该腿。
func TestD565_AllocRisk_LeverageUnknown(t *testing.T) {
	f, s := d490Fixture(d490Snaps(2000, 1000, "USD", time.Minute), 0.01)
	// 读败 → perp 腿 unknown
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) {
		return 0, errors.New("venue leverage read failed")
	}
	resp := d540DryRun(t, f, s, &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingUsd: d490f(100)})
	if resp.RiskLevel != "unknown" {
		t.Fatalf("杠杆读败应 risk=unknown, got %q reasons=%v", resp.RiskLevel, resp.RiskReasons)
	}
	found := false
	for _, r := range resp.RiskReasons {
		if strings.Contains(r, "杠杆实况不可得") {
			found = true
		}
	}
	if !found {
		t.Fatalf("reasons 缺「杠杆实况不可得」: %v", resp.RiskReasons)
	}
	// 恢复实读 10x → 回到 ok（对照证 unknown 由杠杆读败驱动）
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 10, nil }
	resp2 := d540DryRun(t, f, s, &v1.ApproveCarryGraduationRequest{GraduationId: 5, FundingUsd: d490f(100)})
	if resp2.RiskLevel == "unknown" {
		t.Fatalf("杠杆实读恢复后不应 unknown: %v", resp2.RiskReasons)
	}
}

// TestD565_ComboPoolVerdict_StaleSnap T3：非 live 快照 >5min → verdict=unknown
// （池缺同径不谎报锁死）；fresh 快照维持原三态语义。
func TestD565_ComboPoolVerdict_StaleSnap(t *testing.T) {
	ctx := context.Background()
	s := d563VerdictSvc(t, 1000, 1000)
	f, _ := s.gradStore.(*fakeGradDB)
	f.facts = map[string][]fact.Fact{
		"3001157|": {{Kind: fact.KindInitMarginRate, Venue: "3001157", Value: 0.02, Ts: time.Now()}},
	}
	notional := 100.0
	c := pgstore.XAssetComboAdminRow{
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5", NotionalUSD: &notional,
	}
	// MT 池=快照行超龄 10min → unknown（池快照过期）
	stale := d563MtSnap(5000, 4000)
	stale.FetchedAt = time.Now().Add(-10 * time.Minute)
	if v, d := s.comboPoolVerdict(ctx, c, []pgstore.VenueAccountLatest{stale}); v != "unknown" || !strings.Contains(d, "快照过期") {
		t.Fatalf("超龄快照应 unknown, got %s (%s)", v, d)
	}
	// fresh 快照 → 不再被龄闸拦（gate 腿保证金率判据 unknown=既有 D-564 语义）
	if v, d := s.comboPoolVerdict(ctx, c, []pgstore.VenueAccountLatest{d563MtSnap(5000, 4000)}); v != "unknown" || strings.Contains(d, "快照过期") {
		t.Fatalf("fresh 快照不应触发龄闸, got %s (%s)", v, d)
	}
}

// TestD565_MarginFloor_SkippedLegCount T6：OPEN 腿报价缺→跳腿+判定明细透出计数；
// 有报价腿计入分母，无报价腿不静默漏算。
func TestD565_MarginFloor_SkippedLegCount(t *testing.T) {
	st := storetest.SharedStore(t, "ARB_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var posID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('XASSET_CARRY:XAUUSD:a:b','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	// 腿1=XAUUSDm（有报价）；另一仓腿=XAGUSDm（无报价→跳）
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
		 VALUES ($1,0,'OPEN','3001157','mt5','XAUUSDm','BUY',10,2000,'FILLED')`, posID); err != nil {
		t.Fatalf("seed leg1: %v", err)
	}
	var pos2 int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('XASSET_CARRY:XAGUSD:c:d','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&pos2); err != nil {
		t.Fatalf("seed pos2: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
		 VALUES ($1,0,'OPEN','3001157','mt5','XAGUSDm','BUY',10,30,'FILLED')`, pos2); err != nil {
		t.Fatalf("seed leg2: %v", err)
	}
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 2000, Ask: 2000.1})
	s := &Service{store: st, qbus: qb}
	s.SetLegContractSize(func(string, string) (float64, bool) { return 1, true })
	s.mtSummaryFn = func(context.Context, string) (float64, float64, string, error) {
		return 1000, 500, "USD", nil
	}
	_, _, d := s.marginFloorEval(ctx, "3001157:mt5", 4000)
	if !strings.Contains(d, "跳过 1 腿") {
		t.Fatalf("明细应透出跳腿计数, got %q", d)
	}
	// 全腿无报价 → 无可判分母 → known=false 数据缺不判 + 跳腿数透出（≠无腿静默）
	s2 := &Service{store: st, qbus: bus.New()}
	s2.SetLegContractSize(func(string, string) (float64, bool) { return 1, true })
	s2.mtSummaryFn = func(context.Context, string) (float64, float64, string, error) {
		return 1000, 500, "USD", nil
	}
	_, known, d2 := s2.marginFloorEval(ctx, "3001157:mt5", 4000)
	if known || !strings.Contains(d2, "skipped") {
		t.Fatalf("全腿跳应 known=false 且透出跳腿数, got known=%v %s", known, d2)
	}
}

// d490FeedLive D-565 S4 夹具喂料：snaps 值同供实时面——CEX=活账本 ws 实推、
// MT=AccountSummary RPC 缝（eq/fm/ccy）。缺 free→回 equity。
func d490FeedLive(s *Service, snaps []pgstore.VenueAccountLatest, now time.Time) {
	l := bus.NewBalanceLedger()
	s.SetBalanceLedger(l)
	for _, sn := range snaps {
		eq := 0.0
		if sn.EquityRaw != nil {
			eq = *sn.EquityRaw
		}
		av := eq
		if sn.FreeMarginRaw != nil {
			av = *sn.FreeMarginRaw
		}
		if sn.VenueType == "mt4" || sn.VenueType == "mt5" {
			s.mtSummaryFn = func(context.Context, string) (float64, float64, string, error) {
				return eq, av, sn.Currency, nil
			}
			continue
		}
		l.Put(keydomain.Join(sn.Venue, sn.VenueType), av, eq, sn.Currency, "ws", now)
	}
}
