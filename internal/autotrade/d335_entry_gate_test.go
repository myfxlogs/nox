package autotrade

// D-335 T1-T3：XASSET 入口安全边际闸（monitor 单源，pair=CanonicalSell-Canonical）。
// harness 仿 d311 XASSET 前置拦截锚（白名单命中+regime 健康样本过 D-328 闸→进本闸）。
// 观测点：闸在 computeBuyQty 前——skip 则 csMiss=0；放行则走到 sizing（csMiss=1，
// dashboard.New 空 bus 下 USDPerLotForLeg 不可得=既有 fail-closed）。
import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

func d335SeedRegime(t *testing.T, s *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 13; i++ {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding', 'binance', 'XAUUSDT', $1, 'pct_annualized', $2, 'd335test')`,
			5.0+float64(i), time.Now().Add(-time.Duration(13-i)*5*time.Minute)); err != nil {
			t.Fatalf("seed funding fact %d: %v", i, err)
		}
	}
}

func d335SeedMonitor(t *testing.T, s *pgstore.Store, pair string, entry, noise *float64) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_xasset_monitor (pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, cex_venue, mt_venue)
		VALUES ($1, 4355, 4356, 4354, 4355, $2, 5, 3, 0.001, 21.9, 1.0, 24.9, $3,
			'binance:perp','277842155:mt5')`, // D-423：venue 键=fixture 机会 venue
		pair, entry, noise); err != nil {
		t.Fatalf("seed monitor: %v", err)
	}
}

func floatPtr(f float64) *float64 { return &f }

// d335Engine 仿 d311 mkEngine（白名单命中+regime 配置；strategy disabled 由用例自seed）。
// d335Engine（D-563 S4a 涟漪适配：entry 价差=双腿实时报价现算，monitor 行只供 noise）；
// entryBps 参数驱动 cex bid=mtAsk×(1+bps/1e4) 报价播种。
func d335Engine(s *pgstore.Store, entryBps float64) (*Engine, pgstore.Opportunity, context.Context) {
	ctx := context.Background()
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5",
		Bid: 4354, Ask: 4355, Time: time.Now()})
	qb.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "binance", VenueType: "perp",
		Bid: 4355 * (1 + entryBps/1e4), Ask: 4355*(1+entryBps/1e4) + 0.5, Time: time.Now()})
	svc := dashboard.New(nil, qb, nil, nil, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, nil, 0, nil, nil, 0, 0, nil)
	e := &Engine{
		store: s, service: svc, quotes: qb, gate: NewGate(s, Config{}, nil),
		csMissCounts: map[string]int{}, csMissSent: map[string]time.Time{},
		cfg: Config{
			MinNetBps:   0,
			NotionalUSD: 1000,
			AllowVenues: map[string]bool{"277842155:mt5": true, "binance:perp": true},
			XAssetAllowCombos: []exec.XAssetComboAllowSpec{{
				CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT",
				VenueBuy: "277842155:mt5", VenueSell: "binance:perp",
				MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUSD: 10000,
			}},
			CarryRegime: exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12},
		},
	}
	opp := pgstore.Opportunity{
		ID: "XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp", Type: "XASSET_CARRY",
		Canonical: "XAUUSD", CanonicalSell: d311strptr("XAUUSDT"),
		VenueBuy: "277842155:mt5", VenueSell: "binance:perp", NetBps: 18.09,
	}
	return e, opp, ctx
}

func TestD335_EntryGate(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE strategy_stats CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	d335SeedRegime(t, s)
	d330SeedDisabled(t, s) // 复用 D-331 helper：gate check2 早拒=intent 产出观测点

	// T1：entry=5 ≤ noise=9 → 闸拦（不产 intent）；红=现码无闸穿到 sizing（csMiss=1）
	e, opp, ctx := d335Engine(s, 5)
	d335SeedMonitor(t, s, "XAUUSDT-XAU", floatPtr(5), floatPtr(9))
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if e.csMissCounts["277842155:mt5|XAUUSD"] != 0 {
		t.Errorf("T1 噪声带内应 skip 于 sizing 前（红=现码无闸 csMiss=1）, got %v", e.csMissCounts)
	}
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Errorf("T1 不应产 intent, got %d 行", n)
	}

	// T2：entry=12 > noise=9 → 不被本闸拦（走到 sizing 既有 fail-closed=csMiss 1；pass-through 锚）
	e2, opp2, ctx2 := d335Engine(s, 12)
	d335SeedMonitor(t, s, "XAUUSDT-XAU", floatPtr(12), floatPtr(9))
	e2.handleOpportunity(ctx2, opp2, newComboAccumulator())
	if e2.csMissCounts["277842155:mt5|XAUUSD"] != 1 {
		t.Errorf("T2 顺价>noise 应放行到 sizing（csMiss=1）, got %v", e2.csMissCounts)
	}

	// T3a：entry=−3 逆价 → skip（csMiss=0）
	e3, opp3, ctx3 := d335Engine(s, -3)
	d335SeedMonitor(t, s, "XAUUSDT-XAU", floatPtr(-3), floatPtr(9))
	e3.handleOpportunity(ctx3, opp3, newComboAccumulator())
	if e3.csMissCounts["277842155:mt5|XAUUSD"] != 0 {
		t.Errorf("T3a 逆价应 skip（红=现码无闸 csMiss=1）, got %v", e3.csMissCounts)
	}

	// T3b：无 monitor 行 → D-423 fail-closed 拒开（csMiss=0；借用他所簿=假安全边际废止）。
	// 清表防前序子用例行残留（同仓 latest 行会串场）。
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_xasset_monitor`); err != nil {
		t.Fatalf("truncate monitor: %v", err)
	}
	e4, opp4, ctx4 := d335Engine(s, 5)
	e4.handleOpportunity(ctx4, opp4, newComboAccumulator())
	if e4.csMissCounts["277842155:mt5|XAUUSD"] != 0 {
		t.Errorf("T3b 无 monitor 行应 fail-closed 拒开（D-423）, got %v", e4.csMissCounts)
	}
	if n := d330CountLogs(t, s, opp4.ID); n != 0 {
		t.Errorf("T3b 不应产 intent, got %d 行", n)
	}

	// T3c：noise NULL（未成噪）→ D-423 同 fail-closed 拒开
	if _, err := s.Pool().Exec(ctx, `TRUNCATE fin_xasset_monitor`); err != nil {
		t.Fatalf("truncate monitor: %v", err)
	}
	e5, opp5, ctx5 := d335Engine(s, 5)
	d335SeedMonitor(t, s, "XAUUSDT-XAU", floatPtr(5), nil)
	e5.handleOpportunity(ctx5, opp5, newComboAccumulator())
	if e5.csMissCounts["277842155:mt5|XAUUSD"] != 0 {
		t.Errorf("T3c noise NULL 应 fail-closed 拒开（D-423）, got %v", e5.csMissCounts)
	}
}
