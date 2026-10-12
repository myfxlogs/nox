package autotrade

// D-311 S8 锚⑧⑨⑩ 回补段（挂起件函数级回补；recalc 段已先随既有测试件在树）：
// canonical cap 抬升夹具 + venueIsReal CEX/MT 合并视图 + handleOpportunity 前置拦截。

import (
	"context"
	"testing"
	"time"

	"arb/internal/api/dashboard"
	"arb/internal/data/bus"
	"arb/internal/risk/gate"
	"arb/internal/store/pgstore"
	"arb/internal/trading/exec"
)

// d311SeedXAssetPosition 建 XASSET_CARRY 持仓+双腿。
// buyLeg=(buyVenue,buySym) SELL 侧同理；oppID 组合键须与 canonicalBuy 一致。

func d006LiftCanonicalCap(t *testing.T, s *pgstore.Store, canons ...string) {
	t.Helper()
	for _, c := range canons {
		if err := s.UpsertCanonicalExposureCap(context.Background(),
			&pgstore.CanonicalExposureCap{Canonical: c, CapUSD: 1e9, Enabled: true, Note: "test-lift"}); err != nil {
			t.Fatalf("lift canonical cap %s: %v", c, err)
		}
	}
}

// TestD311_RecalcXAssetNetCarry_BuyMT5SellPerp S8锚⑧a：XPTUSD 形态——
// 买 MT5（swap spec 2.0 bps/日 → +7.3%/yr）+ 卖 OKX perp（funding −10%/yr → 空头收 −10）。
// netCarry = 7.3 − 10 = −2.7（持有净收入口径：SELL 腿 +rate）。

// TestD311_HandleOpportunityXAssetPreIntercept S8锚⑩：XASSET_CARRY 前置拦截——
// 无白名单命中 → 不产 intent 不落 REJECTED（修 462 条/时刷屏）；
// 命中 → 过拦截进入下游（csMiss 计数证明越过了拦截点）。

func TestD311_HandleOpportunityXAssetPreIntercept(t *testing.T) {
	s := testAccumStore(t)
	d311Truncate(t, s)
	d006LiftCanonicalCap(t, s, "WTIUSD", "BTCUSDT")
	ctx := context.Background()
	// D-328 S3 涟漪适配：case③ 白名单命中后须过 regime 闸才到 sizing——种卖腿（gate:perp×BTCUSDT）
	// 健康正样本（requiredSign=+1 全程 ≥floor）。
	for i := 0; i < 13; i++ {
		if _, err := s.Pool().Exec(ctx,
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding', 'gate', 'BTCUSDT', $1, 'pct_annualized', $2, 'd311test')`,
			5.0+float64(i), time.Now().Add(-time.Duration(13-i)*5*time.Minute)); err != nil {
			t.Fatalf("seed funding fact %d: %v", i, err)
		}
	}
	opp := pgstore.Opportunity{
		ID: "XASSET_CARRY:WTIUSD:3001157:mt5:gate:perp", Type: "XASSET_CARRY",
		Canonical: "WTIUSD", CanonicalSell: d311strptr("BTCUSDT"),
		VenueBuy: "3001157:mt5", VenueSell: "gate:perp", NetBps: 50,
	}
	countLogs := func() int {
		var n int
		if err := s.Pool().QueryRow(ctx,
			`SELECT count(*) FROM autotrade_logs WHERE opportunity_id = $1`, opp.ID).Scan(&n); err != nil {
			t.Fatalf("count logs: %v", err)
		}
		return n
	}
	mkEngine := func(allow []exec.XAssetComboAllowSpec) *Engine {
		// dashboard.New 注入空 QuoteBus（裸 &Service{} 的 nil qbus 会在 USDPerLotForLeg panic）。
		// D-563 S4a 涟漪适配：同一 qb 供 svc 定尺寸+engine 实时价差闸——
		// 双腿新鲜报价 + cex bid 60.5 vs mt ask 60（entry≈83bps > monitor noise 9）。
		qb := bus.New()
		qb.Publish(bus.Quote{Symbol: "WTIUSD", Venue: "3001157", VenueType: "mt5", Bid: 59.9, Ask: 60.0, Time: time.Now()})
		qb.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", Bid: 60.5, Ask: 60.6, Time: time.Now()})
		svc := dashboard.New(nil, qb, nil, nil, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, 0, nil, nil, 0, 0, nil, 0)
		return &Engine{
			store: s, service: svc, quotes: qb,
			csMissCounts: map[string]int{}, csMissSent: map[string]time.Time{},
			cfg: gate.Config{
				MinNetBps:         0,
				AllowVenues:       map[string]bool{"3001157:mt5": true, "gate:perp": true},
				XAssetAllowCombos: allow,
				// D-328 S3 涟漪适配：regime 闸在白名单之后，不配置=fail-closed 会拦住 case③；
				// 配置 + 下方种健康样本保持本锚原语义（白名单拦截点验证）。
				CarryRegime: exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12},
			},
		}
	}
	// ① 空表（零信任全拒）→ 不产 intent、不落 REJECTED
	e := mkEngine(nil)
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if n := countLogs(); n != 0 {
		t.Fatalf("空白名单应静默跳过, autotrade_logs 落了 %d 行", n)
	}
	// ② 非空表不命中 → 同静默跳过
	e = mkEngine([]exec.XAssetComboAllowSpec{
		{CanonicalBuy: "WTIUSD", CanonicalSell: "ETHUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp"},
	})
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if n := countLogs(); n != 0 {
		t.Fatalf("白名单不命中应静默跳过, autotrade_logs 落了 %d 行", n)
	}
	// ③ 命中 → 过拦截（走到 computeBuyQty → USDPerLotForLeg 不可用 → recordCSMiss）。
	// csMissCounts 递增=拦截已放行的直接证据；仍不落 REJECTED（CS-miss 是聚合告警非拒绝）。
	// D-423 涟漪适配：白名单命中后须过 venue 三元组入口闸才到 sizing——种本组合
	// （pair=BTCUSDT-WTI,cex=gate:perp,mt=3001157:mt5）顺价>noise 行。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_xasset_monitor (pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, cex_venue, mt_venue)
		VALUES ('BTCUSDT-WTI', 4355, 4356, 4354, 4355, 12, 5, 3, 0.001, 21.9, 1.0, 24.9, 9,
			'gate:perp','3001157:mt5')`); err != nil {
		t.Fatalf("seed monitor: %v", err)
	}
	e = mkEngine([]exec.XAssetComboAllowSpec{
		{CanonicalBuy: "WTIUSD", CanonicalSell: "BTCUSDT", VenueBuy: "3001157:mt5", VenueSell: "gate:perp", MaxLongQty: 1, MaxShortQty: 1000, MaxGrossUSD: 50000},
	})
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if e.csMissCounts["3001157:mt5|WTIUSD"] != 1 {
		t.Errorf("白名单命中应过拦截进入下游（csMiss=1）, got %v", e.csMissCounts)
	}
	if n := countLogs(); n != 0 {
		t.Fatalf("CS-miss 路径不应落 REJECTED, autotrade_logs 落了 %d 行", n)
	}
}
