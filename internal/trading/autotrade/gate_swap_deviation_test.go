// gate_swap_deviation_test.go —— D-165 S1：swap 偏差拦截单测（gate.Gate.Submit 路径）。
// JPY 腿 notional 改 usdnotional.PerLot，测试需注入 Quotes + USDJPY 报价。
package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/data/fact"
	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// setupSwapDeviationStore 构造真库 store + autotrade_state=RUNNING + broker_accounts + 已有 OPEN 持仓 + legs + swap snapshots + facts。
// factsSwapBps：BUY 腿 swap_long facts 值（bps）；actualSwapUSD：broker 实际 swap 日增量（USD）。
func setupSwapDeviationStore(t *testing.T, factsSwapBps, actualSwapUSD float64) *pgstore.Store {
	t.Helper()
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, fin_strategy, autotrade_logs, positions, position_legs, position_cashflows, broker_accounts, facts, position_swap_snapshots CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, fin_strategy, autotrade_logs, positions, position_legs, position_cashflows, broker_accounts, facts, position_swap_snapshots CASCADE`)
	})
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	// broker_accounts（demo）
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
		 VALUES ('vA', 'mt5', 'h', 443, 'p', 's', 'connected', false),
		        ('vB', 'mt5', 'h', 443, 'p', 's', 'connected', false)`); err != nil {
		t.Fatalf("insert broker_accounts: %v", err)
	}
	// 已有 OPEN 持仓（同 venue vA）
	now := time.Now().UTC()
	var posID int64
	err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at) VALUES ('SWAPARB:AUDJPY:vA:mt5:vB:mt5', 'SWAPARB', 'OPEN', $1) RETURNING id`,
		now).Scan(&posID)
	if err != nil {
		t.Fatalf("insert position: %v", err)
	}
	// legs：BUY vA:mt5 + SELL vB:mt5
	priceA := 100.0
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, venue, venue_type, symbol, side, qty, price_expected, price_actual, status, ts)
		 VALUES ($1, 0, 'OPEN', 'vA','mt5', 'AUDJPY', 'BUY', 0.01, 100, $2, 'FILLED', $3),
		        ($1, 1, 'OPEN', 'vB','mt5', 'AUDJPY', 'SELL', 0.01, 100, $2, 'FILLED', $3)`,
		posID, priceA, now); err != nil {
		t.Fatalf("insert legs: %v", err)
	}
	// swap snapshots：昨日 0 + 今日 actualSwapUSD（DailySwapAccrued = today - yesterday）
	// 快照 venue 必须是完整 venueKey（与生产写入口径一致，legs.Venue 同口径）。
	yesterday := now.AddDate(0, 0, -1)
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_swap_snapshots (position_id, venue, venue_type, swap_usd, ts) VALUES ($1, 'vA','mt5', 0, $2), ($1, 'vA','mt5', $3, $4)`,
		posID, yesterday, actualSwapUSD, now); err != nil {
		t.Fatalf("insert swap snapshots: %v", err)
	}
	// facts：swap_long vA = factsSwapBps
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ($1, 'vA', 'AUDJPY', $2, 'bps_daily', $3, 'test')`,
		fact.KindSwapLong, factsSwapBps, now); err != nil {
		t.Fatalf("insert swap_long fact: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src) VALUES ($1, 'vB', 'AUDJPY', 0, 'bps_daily', $2, 'test')`,
		fact.KindSwapShort, now); err != nil {
		t.Fatalf("insert swap_short fact: %v", err)
	}
	return s
}

// setupSwapDeviationQuotes D-190：构造带 USDJPY 报价的 QuoteBus（JPY 腿 usdnotional.PerLot 用）。
// USDJPY=150 → AUDJPY CS=1 price=100 → usdPerLot=100/150≈0.667。
func setupSwapDeviationQuotes() *bus.QuoteBus {
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "USDJPY", Bid: 149.9, Ask: 150.1, Time: time.Now()})
	return qb
}

// TestSwapDeviationGate_HighDeviation_Rejected D-165 S1：偏差 > 50% → REJECTED。
// facts=0 bps（notional=1×100=100，factsUSD=0），actual=-2.21 USD → 偏差 100% > 50% → 拒绝。
func TestSwapDeviationGate_HighDeviation_Rejected(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.0, -2.21)
	qb := setupSwapDeviationQuotes()
	// cs=1 静默回退已删——显式 resolver 钉 CS=1 保持原拒语义。
	g := gate.NewGate(s, gate.Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0, SwapArbAllowCombos: nil, Quotes: qb,
		LegContractSizeResolver: csResolver(map[string]float64{"vA:mt5": 1, "vB:mt5": 1})}, nil)
	intent := gate.TradeIntent{
		Strategy:  "CROSS_VENUE",
		Canonical: "AUDJPY",
		VenueBuy:  "vA:mt5",
		VenueSell: "vB:mt5",
		Qty:       0.01,
		DedupKey:  "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			t.Error("Execute should not be called (swap deviation reject)")
			return "FAILED", "should not reach", 0
		},
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status != "REJECTED" {
		t.Errorf("high deviation: got %s, want REJECTED (reason=%s)", res.Status, res.Reason)
	}
	if !strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("reason should contain 'swap deviation gate', got %s", res.Reason)
	}
}

// TestSwapDeviationGate_LowDeviation_Pass D-165 S1：偏差 < 50% → 放行（到下一检查）。
// facts=0.5 bps（notional=1，factsUSD=0.005），actual=0.005 USD → 偏差 0% < 50% → 放行。
func TestSwapDeviationGate_LowDeviation_Pass(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.5, 0.005)
	qb := setupSwapDeviationQuotes()
	g := gate.NewGate(s, gate.Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0, Quotes: qb}, nil)
	intent := gate.TradeIntent{
		Strategy:  "CROSS_VENUE",
		Canonical: "AUDJPY",
		VenueBuy:  "vA:mt5",
		VenueSell: "vB:mt5",
		Qty:       0.01,
		DedupKey:  "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			return "EXECUTED", "", 1
		},
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("low deviation should pass, but rejected: %s", res.Reason)
	}
}

// TestSwapDeviationGate_NoHistory_Pass D-165 S1：无同 venue OPEN 持仓 → 放行（首次开仓走 S3）。
func TestSwapDeviationGate_NoHistory_Pass(t *testing.T) {
	s := testAccumStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, fin_strategy, autotrade_logs, positions, position_legs, position_cashflows, broker_accounts, facts, position_swap_snapshots CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats, fin_strategy, autotrade_logs, positions, position_legs, position_cashflows, broker_accounts, facts, position_swap_snapshots CASCADE`)
	})
	if _, err := s.Pool().Exec(ctx, `INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert autotrade_state: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, venue_type, host, port, password, server, status, is_real)
		 VALUES ('vA', 'mt5', 'h', 443, 'p', 's', 'connected', false),
		        ('vB', 'mt5', 'h', 443, 'p', 's', 'connected', false)`); err != nil {
		t.Fatalf("insert broker_accounts: %v", err)
	}
	// 无 positions → 无历史持仓
	g := gate.NewGate(s, gate.Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 50.0, Quotes: setupSwapDeviationQuotes()}, nil)
	intent := gate.TradeIntent{
		Strategy:  "CROSS_VENUE",
		Canonical: "AUDJPY",
		VenueBuy:  "vA:mt5",
		VenueSell: "vB:mt5",
		Qty:       0.01,
		DedupKey:  "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			return "EXECUTED", "", 1
		},
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("no history should pass, but rejected: %s", res.Reason)
	}
}

// TestSwapDeviationGate_Disabled_Pass D-165 S1 反证：SwapDeviationGatePct=0（禁用）→ 高偏差也放行。
func TestSwapDeviationGate_Disabled_Pass(t *testing.T) {
	s := setupSwapDeviationStore(t, 0.0, -2.21)
	qb := setupSwapDeviationQuotes()
	g := gate.NewGate(s, gate.Config{MaxDailyLoss: 100, MaxPositions: 5, LiveTrade: true, SwapDeviationGatePct: 0, Quotes: qb}, nil) // 禁用
	intent := gate.TradeIntent{
		Strategy:  "CROSS_VENUE",
		Canonical: "AUDJPY",
		VenueBuy:  "vA:mt5",
		VenueSell: "vB:mt5",
		Qty:       0.01,
		DedupKey:  "CROSS_VENUE:AUDJPY:vA:mt5:vB:mt5",
		Execute: func(ctx context.Context) (string, string, int64) {
			return "EXECUTED", "", 1
		},
	}
	res := g.Submit(context.Background(), &intent)
	if res.Status == "REJECTED" && strings.Contains(res.Reason, "swap deviation gate") {
		t.Errorf("disabled (Pct=0) should pass even with high deviation, but rejected: %s", res.Reason)
	}
}

// TestSwapDeviationBetween 纯函数边界测试。
func TestSwapDeviationBetween(t *testing.T) {
	cases := []struct {
		name   string
		facts  float64
		actual float64
		want   float64
	}{
		{"两者为 0→偏差 0", 0, 0, 0},
		{"facts=0 actual非0→偏差 100", 0, -2.21, 100},
		{"同号同值→偏差 0", 0.5, 0.5, 0},
		{"异号→偏差 200", 0.5, -0.5, 200},           // |0.5-(-0.5)|=1.0, max(0.5,0.5,0.01)=0.5, 1.0/0.5*100=200
		{"极小值→偏差受 0.01 下限保护", 0.001, 0.002, 10}, // |0.001-0.002|/max(0.001,0.002,0.01)=0.001/0.01*100=10
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := gate.SwapDeviationBetween(c.facts, c.actual)
			if got < c.want-0.1 || got > c.want+0.1 {
				t.Errorf("gate.SwapDeviationBetween(%v, %v) = %.2f, want ~%.2f", c.facts, c.actual, got, c.want)
			}
		})
	}
}
