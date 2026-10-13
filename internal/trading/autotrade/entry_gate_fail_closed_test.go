package autotrade

// D-423 engine 段回补（M2.3 挂起件函数级回补，D-223⑤/D-226②）：EntryGate fail-closed + 反向组合拒开。

import (
	"context"
	"testing"

	"nox/internal/trading/exec"
)

func TestD423_EntryGateFailClosed(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	if _, err := s.Pool().Exec(ctx, `TRUNCATE strategy_stats, fin_strategy, fin_xasset_monitor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	d335SeedRegime(t, s)
	d330SeedDisabled(t, s)
	e, opp, ctx := d335Engine(s, 12) // opp=277842155:mt5×binance:perp
	// 只种错 venue 行（okx:perp×277842155:mt5）——pair 同但 cex_venue 不同不得命中。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_xasset_monitor (pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, cex_venue, mt_venue)
		VALUES ('XAUUSDT-XAU', 4355, 4356, 4354, 4355, 12, 5, 3, 0.001, 21.9, 1.0, 24.9, 9,
			'okx:perp','277842155:mt5')`); err != nil {
		t.Fatalf("seed wrong-venue: %v", err)
	}
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if e.csMissCounts["277842155:mt5|XAUUSD"] != 0 {
		t.Errorf("binance 簿缺（仅 okx 行）应 fail-closed 拒开不借用, got %v", e.csMissCounts)
	}
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Errorf("拒开不应产 intent, got %d 行", n)
	}
}

// TestD423_ReverseComboRejected 反向组合钉死：VenueBuy=CEX（CEX 为买腿/SELL 为 MT）
// monitor 方向语义未定义 → 拒开（红=闸不查方向照样放行）。
func TestD423_ReverseComboRejected(t *testing.T) {
	ctx := context.Background()
	s := testAccumStore(t)
	d311Truncate(t, s)
	if _, err := s.Pool().Exec(ctx, `TRUNCATE strategy_stats, fin_strategy, fin_xasset_monitor CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	d335SeedRegime(t, s)
	d330SeedDisabled(t, s)
	e, opp, ctx := d335Engine(s, 12)
	// 反向：买腿=binance:perp（CEX），卖腿=277842155:mt5（MT）——方向未定义拒开。
	opp.VenueBuy, opp.VenueSell = "binance:perp", "277842155:mt5"
	opp.ID = "XASSET_CARRY:XAUUSD:binance:perp:277842155:mt5"
	e.cfg.XAssetAllowCombos = []exec.XAssetComboAllowSpec{{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT",
		VenueBuy: "binance:perp", VenueSell: "277842155:mt5",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUSD: 10000,
	}}
	// 即便同 pair 有 monitor 行（任意 venue）也不得放行——方向闸先行。
	d335SeedMonitor(t, s, "XAUUSDT-XAU", floatPtr(12), floatPtr(9))
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if len(e.csMissCounts) != 0 {
		t.Errorf("反向组合应拒开（未定义 monitor 方向）, csMiss=%v", e.csMissCounts)
	}
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Errorf("反向组合不应产 intent, got %d 行", n)
	}
}
