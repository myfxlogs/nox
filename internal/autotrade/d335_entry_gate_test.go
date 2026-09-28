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
func d335Engine(s *pgstore.Store) (*Engine, pgstore.Opportunity, context.Context) {
	ctx := context.Background()
	svc := dashboard.New(nil, bus.New(), nil, nil, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, nil, nil, nil, 0, nil, nil, 0, 0, nil)
	e := &Engine{
		store: s, service: svc, quotes: bus.New(), gate: NewGate(s, Config{}, nil),
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

	// D-562 S2 重写：安全边际闸=qbus 双腿实时报价（monitor 簿退位统计面——三禁令②③）。
	// T1：实时顺价 ≤0（cex bid < mt ask 逆价）→ 闸拦（csMiss=0，不产 intent）。
	// monitor 簿种顺价行也不得影响判定（簿值被无视=禁令锚）。
	e, opp, ctx := d335Engine(s)
	d335SeedMonitor(t, s, "XAUUSDT-XAU", floatPtr(12), floatPtr(9)) // 簿值顺价 12——仍须被无视
	e.quotes.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "binance", VenueType: "perp", VenueSymbol: "XAUUSDT", Bid: 4350, Ask: 4351, Time: time.Now()})
	e.quotes.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", VenueSymbol: "XAUUSD", Bid: 4354, Ask: 4355, Time: time.Now()})
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if e.csMissCounts["277842155:mt5|XAUUSD"] != 0 {
		t.Errorf("T1 实时逆价（4350<4355）应闸拦，monitor 簿顺价被无视（三禁令锚）, got %v", e.csMissCounts)
	}
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Errorf("T1 不应产 intent, got %d 行", n)
	}

	// T2：实时顺价>0（binance bid 4360 > mt ask 4355 → +11.48bps）→ 放行到 sizing（csMiss=1）。
	e2, opp2, ctx2 := d335Engine(s)
	e2.quotes.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "binance", VenueType: "perp", VenueSymbol: "XAUUSDT", Bid: 4360, Ask: 4361, Time: time.Now()})
	e2.quotes.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", VenueSymbol: "XAUUSD", Bid: 4354, Ask: 4355, Time: time.Now()})
	e2.handleOpportunity(ctx2, opp2, newComboAccumulator())
	if e2.csMissCounts["277842155:mt5|XAUUSD"] != 1 {
		t.Errorf("T2 实时顺价应放行到 sizing（csMiss=1）, got %v", e2.csMissCounts)
	}

	// T3a：双腿报价缺（qbus 空）→ fail-closed 拒开（csMiss=0；缺实时证据不判——禁令③）。
	e4, opp4, ctx4 := d335Engine(s)
	e4.handleOpportunity(ctx4, opp4, newComboAccumulator())
	if e4.csMissCounts["277842155:mt5|XAUUSD"] != 0 {
		t.Errorf("T3a 无实时报价应 fail-closed 拒开, got %v", e4.csMissCounts)
	}
	if n := d330CountLogs(t, s, opp4.ID); n != 0 {
		t.Errorf("T3a 不应产 intent, got %d 行", n)
	}

	// T3b：monitor 簿行在场但报价缺 → 仍拒（簿不得补位=禁令②锚）。
	d335SeedMonitor(t, s, "XAUUSDT-XAU", floatPtr(12), floatPtr(9))
	e5, opp5, ctx5 := d335Engine(s)
	e5.handleOpportunity(ctx5, opp5, newComboAccumulator())
	if e5.csMissCounts["277842155:mt5|XAUUSD"] != 0 {
		t.Errorf("T3b 簿在场报价缺仍应拒（禁令②），got %v", e5.csMissCounts)
	}
}
