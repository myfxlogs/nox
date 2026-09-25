// d472_margin_warn_test.go —— D-472 S3：资金拒单业主告警节流矩阵（B-005 强制锚）。
// 10 连拒=恰 1 条 warn；非 margin FAILED=0 warn；venue 原文大小写变体命中；liq-distance 命中。
package autotrade

import (
	"context"
	"testing"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/testutil"
)

func TestD472_MarginWarnThrottled(t *testing.T) {
	st, err := pgstore.New(context.Background(), testutil.TestDSN(t, "ARB_TEST_PG_DSN"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `DELETE FROM alerts WHERE message LIKE '[资金拒单]%'`); err != nil {
		t.Fatal(err)
	}
	defer st.Pool().Exec(ctx, `DELETE FROM alerts WHERE message LIKE '[资金拒单]%'`)
	count := func() int {
		var n int
		if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM alerts WHERE level='warn' AND message LIKE '[资金拒单]%'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	g := &Gate{store: st, lastMarginWarn: make(map[string]time.Time)}
	intent := TradeIntent{Strategy: "XASSET_CARRY", Canonical: "XPTUSD", VenueBuy: "3001157:mt5", VenueSell: "gate:perp", NetBps: 18}

	// 锚 A3：同一 margin 拒绝连续 10 次 → alerts 仅 1 行 warn（≥6h 节流）。
	for i := 0; i < 10; i++ {
		g.marginWarnThrottled(ctx, intent, "Abort: binance margin insufficient: need 20.00 > avail 8.00 × 0.80")
	}
	if n := count(); n != 1 {
		t.Fatalf("10 连拒应恰 1 条 warn（≥6h 节流锚），got %d", n)
	}

	// 不同组合=独立节流键 → 新 warn。
	i2 := intent
	i2.Canonical = "XPDUSD"
	g.marginWarnThrottled(ctx, i2, "gate margin insufficient: need 100.00 > avail 40.00 × 0.80")
	if n := count(); n != 2 {
		t.Fatalf("不同组合应独立节流键（新 warn），got %d", n)
	}

	// 非 margin FAILED → 无 warn。
	g.marginWarnThrottled(ctx, intent, "Abort: MARKET_CLOSED")
	if n := count(); n != 2 {
		t.Fatalf("非 margin FAILED 不应告警，got %d", n)
	}

	// venue 拒单原文大小写变体（Margin is insufficient）→ 同规则命中。
	i3 := intent
	i3.Canonical = "BTCUSDT"
	g.marginWarnThrottled(ctx, i3, "Abort: binance rejected: Margin is insufficient for this order")
	if n := count(); n != 3 {
		t.Fatalf("大小写变体应命中（新组合键），got %d", n)
	}

	// 强平闸拒文（liq-distance 子串）→ 同规则命中。
	i4 := intent
	i4.Canonical = "XAUUSD"
	g.marginWarnThrottled(ctx, i4, "Abort: gate liq-distance cross notional/equity=10.000 > cap=0.300")
	if n := count(); n != 4 {
		t.Fatalf("liq-distance 拒文应命中（新组合键），got %d", n)
	}
}
