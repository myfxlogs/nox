package dashboard

// D-563 锚测：T2 legBare 剥域 / T3 comboPoolVerdict 三态矩阵（杠杆感知腿级保证金）/
// T5 MT 腿资金 AccountSummary 实时化（RPC 败 fail-closed、快照在场仍拒）。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/ledger/reconcile"
	"nox/internal/platform/testutil/storetest"
	"nox/internal/store/pgstore"
)

// ---- T2 legBare 剥域 ----
func TestD563_LegBare(t *testing.T) {
	for _, c := range []struct{ in, suffix, want string }{
		{"3001157:mt5", "mt5", "3001157:mt5"}, // 带域键原样（剥域=监控键恒 miss 假阳性病灶）
		{"gate", "perp", "gate:perp"},
		{"", "perp", ""},
	} {
		if got := legBare(c.in, c.suffix); got != c.want {
			t.Errorf("legBare(%q,%q)=%q want %q", c.in, c.suffix, got, c.want)
		}
	}
}

// ---- T3 comboPoolVerdict 三态矩阵 ----

// d563VerdictSvc gate 池=活账本 ledger 命中（零 REST）；MT 池=快照行；
// gradDB.facts 供 init_margin_rate。
func d563VerdictSvc(t *testing.T, gateAvail, gateEq float64) *Service {
	t.Helper()
	s := &Service{}
	l := bus.NewBalanceLedger()
	s.SetBalanceLedger(l)
	l.Put("gate:perp", gateAvail, gateEq, "USD", "ws", time.Now())
	s.liveQuerierFor = func(context.Context, string) (reconcile.AccountQuerier, string, bool) {
		return &fakeAcctQ{err: errors.New("REST 不应被调（live 命中）")}, "gate:perp", true
	}
	s.gradStore = &fakeGradDB{}
	return s
}

func d563MtSnap(equity, free float64) pgstore.VenueAccountLatest {
	return pgstore.VenueAccountLatest{
		Venue: "3001157", VenueType: "mt5",
		EquityRaw: &equity, FreeMarginRaw: &free,
		Currency: "USD", FetchedAt: time.Now(),
	}
}

func TestD563_ComboPoolVerdict(t *testing.T) {
	ctx := context.Background()
	s := d563VerdictSvc(t, 1000, 1000)
	f, _ := s.gradStore.(*fakeGradDB)
	f.facts = map[string][]fact.Fact{
		"3001157|": {{Kind: fact.KindInitMarginRate, Venue: "3001157", Value: 0.02, Ts: time.Now()}},
	}
	notional := 4300.0
	c := pgstore.XAssetComboAdminRow{
		VenueBuy: "gate:perp", VenueSell: "3001157:mt5", NotionalUSD: &notional,
	}
	snaps := []pgstore.VenueAccountLatest{d563MtSnap(5000, 4000)}

	// D-564 U2 退役→D-572 实读化：gate 腿 venueLeverageFn 未注入→venueLeverageExec
	// 实盘配置缺读败 → unknown 黄标（fail-closed 如实标缺；实读权威=precheck）。
	if v, d := s.comboPoolVerdict(ctx, c, snaps); v != "unknown" || !strings.Contains(d, "杠杆实况不可得") {
		t.Fatalf("gate 腿杠杆读败应 unknown, got %s (%s)", v, d)
	}
	// D-006 S1b：MT init_margin_rate 缺 → 悲观档 20x（0.05）估+注记（不再 unknown），
	// unknown 改由 gate 腿杠杆读败承担。
	f.facts = nil
	if frac, note, known := s.legMarginNeedFrac(ctx, "3001157:mt5", "", false); !known || frac != 0.05 || !strings.Contains(note, "悲观档") {
		t.Fatalf("MT rate 缺应悲观档 0.05+注记: frac=%v note=%q known=%v", frac, note, known)
	}
	if v, d := s.comboPoolVerdict(ctx, c, snaps); v != "unknown" || !strings.Contains(d, "杠杆实况不可得") {
		t.Fatalf("gate 腿读败仍应 unknown: %s (%s)", v, d)
	}
}

// ---- T5 MT 腿资金实时化 ----

func TestD563_MarginFloorMTRealtime(t *testing.T) {
	st := storetest.SharedStore(t, "NOX_TEST_PG_SERVICE_DSN")
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE positions, position_legs RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	var posID int64
	if err := st.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('XASSET_CARRY:XAUUSD:a:b','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed pos: %v", err)
	}
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, status)
		 VALUES ($1,0,'OPEN','3001157','mt5','XAUUSDm','BUY',10,2000,'FILLED')`, posID); err != nil {
		t.Fatalf("seed leg: %v", err)
	}

	qb := bus.New() // D-565 S6：腿级实时报价（OPEN 腿名义价存价回退已删）
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", Bid: 2000, Ask: 2000.1})
	s := &Service{store: st, qbus: qb}
	s.SetLegContractSize(func(string, string) (float64, bool) { return 1, true })
	// ① RPC 死 + 快照行在档 → 仍 fail-closed（快照退决策面实证）。
	if _, err := st.Pool().Exec(ctx,
		`INSERT INTO fin_venue_account_snapshot (venue, venue_type, equity_raw, free_margin_raw, currency, fetched_at)
		 VALUES ('3001157','mt5',99999,99999,'USD',now())`); err != nil {
		t.Fatalf("seed snap: %v", err)
	}
	s.mtSummaryFn = func(context.Context, string) (float64, float64, string, error) {
		return 0, 0, "", errors.New("rpc dead")
	}
	if ok, _, d := s.marginFloorEval(ctx, "3001157:mt5", 0); ok || !strings.Contains(d, "MT 实时资金不可得") {
		t.Fatalf("RPC 死应 fail-closed, got ok=%v %s", ok, d)
	}
	// ② RPC 活：eq=1000 fm=500（used=500）、open notional=10×2000=20000 →
	// implied=500/20000=0.025；add=4000→req 100≤avail 500 放行；add=40000→req 1000>500 拒。
	s.mtSummaryFn = func(context.Context, string) (float64, float64, string, error) {
		return 1000, 500, "USD", nil
	}
	if ok, known, d := s.marginFloorEval(ctx, "3001157:mt5", 4000); !ok || !known {
		t.Fatalf("可用足应放行, got ok=%v known=%v %s", ok, known, d)
	}
	if ok, _, d := s.marginFloorEval(ctx, "3001157:mt5", 40000); ok || !strings.Contains(d, "avail") {
		t.Fatalf("超 floor 应拒, got ok=%v %s", ok, d)
	}
}
