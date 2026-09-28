package autotrade

// D-563 S4a T4：XASSET 入场安全边际改双腿实时报价现算——
// 双腿新鲜→按实时价差判定；任一腿缺/>5min→fail-closed 拒开；noise 阈仍读 monitor。
// 复用 d330 夹具（monitor 行 noise_bps=9 + strategy disabled 捕获 intent）。

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
)

func d563Spec() exec.XAssetComboAllowSpec {
	return exec.XAssetComboAllowSpec{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT",
		VenueBuy: "277842155:mt5", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUSD: 10000,
	}
}

func d563Run(t *testing.T, cexBid float64, cexAge time.Duration, publishCex bool) int {
	t.Helper()
	s := d330Setup(t) // monitor 行 noise=9、entry_spread=12（实时化后仅 noise 仍消费）
	d330SeedDisabled(t, s)
	d330RegimeHealthy(t, s)
	ctx := context.Background()
	svc, qb := d330XAUService(100, 4355) // mt 腿 ask=4355 新鲜
	if publishCex {
		qb.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "binance", VenueType: "perp",
			Bid: cexBid, Ask: cexBid + 0.5, Time: time.Now().Add(-cexAge)})
	}
	e := d330Engine(s, svc, qb, d563Spec())
	opp := d330GoldenOpp()
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	return d330CountLogs(t, s, opp.ID)
}

// 双腿新鲜且实时价差 >noise → 进 intent（disabled 预拒落 1 行观测点）。
func TestD563_LiveGate_PassFreshQuotes(t *testing.T) {
	// bid 4365 vs ask 4355 → entry≈22.96bps > noise 9。
	if n := d563Run(t, 4365, 0, true); n != 1 {
		t.Fatalf("新鲜双腿+价差过阈应产 intent(1 行), got %d", n)
	}
}

// cex 腿报价 >5min → fail-closed 拒开（0 行）。
func TestD563_LiveGate_StaleCexQuote(t *testing.T) {
	if n := d563Run(t, 4365, 10*time.Minute, true); n != 0 {
		t.Fatalf("cex 报价超龄应拒开, got %d", n)
	}
}

// cex 腿无报价 → fail-closed（0 行）。
func TestD563_LiveGate_MissingCexQuote(t *testing.T) {
	if n := d563Run(t, 0, 0, false); n != 0 {
		t.Fatalf("cex 报价缺应拒开, got %d", n)
	}
}

// 实时价差 ≤0（逆价）→ 拒开（0 行）。
func TestD563_LiveGate_InvertedSpread(t *testing.T) {
	if n := d563Run(t, 4350, 0, true); n != 0 {
		t.Fatalf("逆价应拒开, got %d", n)
	}
}

// 实时价差 ≤noise（bid 4356→2.3bps < 9）→ 拒开（0 行）。
func TestD563_LiveGate_WithinNoise(t *testing.T) {
	if n := d563Run(t, 4356, 0, true); n != 0 {
		t.Fatalf("摆幅内应拒开, got %d", n)
	}
}
