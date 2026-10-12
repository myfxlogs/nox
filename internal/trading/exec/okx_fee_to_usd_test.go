package exec

// 锚 D：fee_usd 采集补洞——persistOpenLegs 把 LegResult.FeeUSD（CEX 实采）
// 与 MT commission（负=付出→取绝对值）落 position_legs.fee_usd；拿不到 → NULL 不写 0。
// 先红后绿：改前 CreatePositionLeg 的 FeeUSD 字段恒 nil → fee_usd 恒 NULL。

import (
	"context"
	"testing"

	"arb/internal/platform/testutil"
)

// okxFeeToUSD 纯函数：venue 负号→正数成本；非 USD 系 feeCcy/空/0 → ok=false（NULL）。
func TestD391_OkxFeeToUSD(t *testing.T) {
	cases := []struct {
		fee, ccy string
		want     float64
		ok       bool
	}{
		{"-2.18095", "USDT", 2.18095, true}, // pos498 实证形态
		{"-0.5", "USDC", 0.5, true},
		{"0.03", "USD", 0.03, true}, // 返佣正号也取绝对值（罕见，口径=成本正数）
		{"", "USDT", 0, false},
		{"0", "USDT", 0, false},
		{"-0.001", "BTC", 0, false}, // 非 USD 系不猜换算
		{"abc", "USDT", 0, false},
	}
	for _, c := range cases {
		got, ok := okxFeeToUSD(c.fee, c.ccy)
		if ok != c.ok || (ok && got != c.want) {
			t.Errorf("okxFeeToUSD(%q,%q) = (%v,%v), want (%v,%v)", c.fee, c.ccy, got, ok, c.want, c.ok)
		}
	}
}

// persistOpenLegs 落库锚：CEX 腿 FeeUSD 实采值、MT 腿 commission 负值 → fee_usd 正数成本。
func TestD391_PersistOpenLegs_FeeUSD(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_EXEC_TEST_PG_DSN")
	s := testStore(t)
	conn := seedConn(t, dsn)
	ctx := context.Background()

	var posID int64
	if err := conn.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('opp-d391','XASSET_CARRY','OPEN',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	p := &Pipeline{store: s}
	legs := []LegSpec{
		{VenueKey: "3001157:mt5", VenueSymbol: "XAUUSDm", Side: "BUY", Qty: 0.01, PriceExpected: 4356.8},
		{VenueKey: "okx:perp", VenueSymbol: "XAU-USDT-SWAP", Side: "SELL", Qty: 1000, PriceExpected: 4361.9},
	}
	results := []LegResult{
		{VenueKey: "3001157:mt5", OrderID: "mt-t1", Price: 4356.863, Qty: 0.01, Status: "FILLED", Commission: -0.35},
		{VenueKey: "okx:perp", OrderID: "3943917115077283840", Price: 4361.9, Qty: 1000, Status: "FILLED", FeeUSD: 2.18095},
	}
	if err := p.persistOpenLegs(ctx, "req-d391", posID, legs, results); err != nil {
		t.Fatalf("persistOpenLegs: %v", err)
	}
	var fee0, fee1 *float64
	if err := conn.QueryRow(ctx, `SELECT fee_usd FROM position_legs WHERE position_id=$1 AND leg_no=0`, posID).Scan(&fee0); err != nil {
		t.Fatalf("query leg0: %v", err)
	}
	if err := conn.QueryRow(ctx, `SELECT fee_usd FROM position_legs WHERE position_id=$1 AND leg_no=1`, posID).Scan(&fee1); err != nil {
		t.Fatalf("query leg1: %v", err)
	}
	if fee0 == nil || *fee0 != 0.35 {
		t.Errorf("leg0 fee_usd = %v, want 0.35（MT commission 负值取绝对值）", fee0)
	}
	if fee1 == nil || *fee1 != 2.18095 {
		t.Errorf("leg1 fee_usd = %v, want 2.18095（CEX 实采）", fee1)
	}
}

// 拿不到 fee/commission → fee_usd 写 NULL（不写 0 冒充无费）。
func TestD391_PersistOpenLegs_FeeNull(t *testing.T) {
	dsn := testutil.TestDSN(t, "ARB_EXEC_TEST_PG_DSN")
	s := testStore(t)
	conn := seedConn(t, dsn)
	ctx := context.Background()

	var posID int64
	if err := conn.QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, expected_net_bps) VALUES ('opp-d391-null','CROSS_VENUE','OPEN',0) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	p := &Pipeline{store: s}
	legs := []LegSpec{{VenueKey: "okx:spot", VenueSymbol: "X", Side: "BUY", Qty: 1, PriceExpected: 100}}
	results := []LegResult{{VenueKey: "okx:spot", OrderID: "o1", Price: 100, Qty: 1, Status: "FILLED"}}
	if err := p.persistOpenLegs(ctx, "req-d391b", posID, legs, results); err != nil {
		t.Fatalf("persistOpenLegs: %v", err)
	}
	var fee *float64
	if err := conn.QueryRow(ctx, `SELECT fee_usd FROM position_legs WHERE position_id=$1 AND leg_no=0`, posID).Scan(&fee); err != nil {
		t.Fatalf("query: %v", err)
	}
	if fee != nil {
		t.Errorf("fee_usd want NULL, got %v", *fee)
	}
}
