package dashboard

// d076_pool_verdict_mt_test.go —— D-076 锚测：MT 腿池快照过期改判定时刻实读。
// T1 MT 过期快照+RPC 活 → ok（修前恒 unknown）；T2 RPC 败 → unknown「MT 实时资金不可得」；
// T3 CEX 腿过期路径回归仍「池快照过期」；acctMon 未接线（nil）全程不 panic。

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// d076StaleCombo 两 frac 俱已知（gate perp 杠杆实读 10x、MT init_margin_rate 0.02），
// notional=100 → 各腿 need≪avail；MT 腿池=超龄快照行。
func d076Svc(t *testing.T, mtErr error) (*Service, *fakeGradDB, pgstore.XAssetComboAdminRow, []pgstore.VenueAccountLatest) {
	t.Helper()
	s := d563VerdictSvc(t, 1000, 1000) // gate:perp 池=活账本 live
	f, _ := s.gradStore.(*fakeGradDB)
	f.facts = map[string][]fact.Fact{
		"3001157|": {{Kind: fact.KindInitMarginRate, Venue: "3001157", Value: 0.02, Ts: time.Now()}},
	}
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 10, nil }
	s.SetMTSummaryFunc(func(context.Context, string) (float64, float64, string, error) {
		if mtErr != nil {
			return 0, 0, "", mtErr
		}
		return 4195, 4195, "USD", nil
	})
	notional := 100.0
	c := pgstore.XAssetComboAdminRow{VenueBuy: "gate:perp", VenueSell: "3001157:mt5", NotionalUSD: &notional}
	stale := d563MtSnap(5000, 4000)
	stale.FetchedAt = time.Now().Add(-6 * time.Minute)
	return s, f, c, []pgstore.VenueAccountLatest{stale}
}

// T1：MT 腿快照超龄 6min 且非 live → 判定时刻 RPC 实读 → verdict=ok（修前=unknown 池快照过期）。
// acctMon 未接线（nil）路径顺带覆盖 kick 守卫 nil 安全。
func TestD076_MTPoolStale_LiveRPC(t *testing.T) {
	s, _, c, snaps := d076Svc(t, nil)
	if v, d := s.comboPoolVerdict(context.Background(), c, snaps); v != "ok" {
		t.Fatalf("MT 实读成功应 verdict=ok, got %s (%s)", v, d)
	}
}

// T2：同形态但 RPC 败 → unknown 且 detail 含「MT 实时资金不可得」（fail-closed 不回落快照）。
func TestD076_MTPoolStale_RPCFail(t *testing.T) {
	s, _, c, snaps := d076Svc(t, errors.New("rpc dead"))
	v, d := s.comboPoolVerdict(context.Background(), c, snaps)
	if v != "unknown" || !strings.Contains(d, "MT 实时资金不可得") {
		t.Fatalf("RPC 败应 unknown+MT 实时资金不可得, got %s (%s)", v, d)
	}
}

// T3：CEX 腿（okx:perp）快照过期仍走原径——unknown 含「池快照过期」（只改 MT 腿的回归锚）。
// 同组 MT 腿经新实读径过判定，证明 detail 只剩 CEX 腿的过期注记。
func TestD076_CEXPoolStale_Unchanged(t *testing.T) {
	ctx := context.Background()
	s, _, _, _ := d076Svc(t, nil)
	notional := 100.0
	c := pgstore.XAssetComboAdminRow{VenueBuy: "okx:perp", VenueSell: "3001157:mt5", NotionalUSD: &notional}
	eq, fm := 5000.0, 4000.0
	snaps := []pgstore.VenueAccountLatest{
		{Venue: "okx", VenueType: "perp", EquityRaw: &eq, FreeMarginRaw: &fm, Currency: "USD", FetchedAt: time.Now().Add(-6 * time.Minute)},
		func() pgstore.VenueAccountLatest {
			r := d563MtSnap(5000, 4000)
			r.FetchedAt = time.Now().Add(-6 * time.Minute)
			return r
		}(),
	}
	v, d := s.comboPoolVerdict(ctx, c, snaps)
	if v != "unknown" || !strings.Contains(d, "池快照过期") || !strings.Contains(d, "okx:perp") {
		t.Fatalf("CEX 过期快照应 unknown+池快照过期, got %s (%s)", v, d)
	}
	if strings.Contains(d, "MT 实时资金不可得") || strings.Contains(d, "3001157:mt5（池快照过期") {
		t.Fatalf("MT 腿应走实读径不出过期注记, got %s", d)
	}
}
