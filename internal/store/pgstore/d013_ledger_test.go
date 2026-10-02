// d013_ledger_test.go —— D-013 账本单真相回归锚。
// ① FILLED/EXTERNAL 腿落库即写 leg:<id> commission 行（CEX=−fee、MT 显式 0、幂等）；
// ② PositionNetUSD 单源式 = 腿价差 + funding/swap + 腿键 commission（leg: 键铺位后
//
//	旧 exec:* 键行退出计算），#498 实测回放 = +1.17；
//
// ③ 事故族（legged_unwind/incident_close/incident_attributed/FAILED）从
//
//	AggregateStrategyStats* 与 DailyRealizedPnl* 全出口排除。
package pgstore

import (
	"context"
	"math"
	"strconv"
	"testing"
	"time"
)

func seedD013Pos(t *testing.T, st *Store, opp, status string) int64 {
	t.Helper()
	var id int64
	if err := st.Pool().QueryRow(context.Background(), `INSERT INTO positions
		(opportunity_id, type, status, expected_net_bps, environment, opened_at)
		VALUES ($1,'XASSET_CARRY',$2,10,'live', now()-interval '3d') RETURNING id`,
		opp, status).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func d013Leg(t *testing.T, st *Store, posID int64, legNo int16, phase, venue, side string,
	qty, px float64, status string, fee *float64) PositionLeg {
	t.Helper()
	leg := &PositionLeg{
		PositionID: posID, LegNo: legNo, Phase: phase, AttemptNo: 1,
		Venue: venue, Symbol: "XAUUSDm", Side: side, Qty: qty,
		PriceActual: &px, OrderID: "t", Status: status,
		FilledQty: &qty, AvgFillPrice: &px, FeeUSD: fee,
	}
	if venue == "okx:perp" || venue == "gate:perp" {
		leg.Symbol = "XAU-USDT-SWAP"
	}
	if err := st.CreatePositionLeg(context.Background(), leg); err != nil {
		t.Fatal(err)
	}
	if leg.ID == 0 {
		t.Fatalf("leg id not populated")
	}
	return *leg
}

func legCommRow(t *testing.T, st *Store, legID int64) (amount float64, n int) {
	t.Helper()
	if err := st.Pool().QueryRow(context.Background(),
		`SELECT COALESCE(SUM(amount_usd),0), count(*) FROM position_cashflows
		 WHERE kind='commission' AND event_key=$1`,
		"leg:"+strconv.FormatInt(legID, 10)).Scan(&amount, &n); err != nil {
		t.Fatal(err)
	}
	return
}

// TestD013_CommissionLegKeyed ①：终态腿必写腿键 commission 行。
func TestD013_CommissionLegKeyed(t *testing.T) {
	st := sharedTestStore(t)
	ctx := context.Background()
	st.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_cashflows RESTART IDENTITY CASCADE`)
	t.Cleanup(func() {
		st.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_cashflows RESTART IDENTITY CASCADE`)
	})
	posID := seedD013Pos(t, st, "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp:d013a", "OPEN")
	fee := 2.5
	cex := d013Leg(t, st, posID, 0, "OPEN", "okx:perp", "SELL", 1, 4300, "FILLED", &fee)
	mt := d013Leg(t, st, posID, 1, "OPEN", "3001157:mt5", "BUY", 0.01, 4300, "FILLED", nil)
	ext := d013Leg(t, st, posID, 0, "CLOSE", "okx:perp", "BUY", 1, 4290, "EXTERNAL", nil)
	// CEX −fee；MT 显式 0；EXTERNAL fee NULL 显式 0
	if amt, n := legCommRow(t, st, cex.ID); n != 1 || amt != -2.5 {
		t.Fatalf("cex leg commission row: n=%d amt=%v", n, amt)
	}
	if amt, n := legCommRow(t, st, mt.ID); n != 1 || amt != 0 {
		t.Fatalf("mt leg commission row: n=%d amt=%v", n, amt)
	}
	if amt, n := legCommRow(t, st, ext.ID); n != 1 || amt != 0 {
		t.Fatalf("external leg commission row: n=%d amt=%v", n, amt)
	}
	// 幂等：再 Ensure/重插入不增生
	if err := st.EnsureLegCommissionCashflow(ctx, cex, nil); err != nil {
		t.Fatal(err)
	}
	if _, n := legCommRow(t, st, cex.ID); n != 1 {
		t.Fatalf("idempotent ensure: n=%d", n)
	}
	// 非终态腿不写
	pend := d013Leg(t, st, posID, 1, "CLOSE", "3001157:mt5", "SELL", 0.01, 4290, "PENDING", nil)
	if _, n := legCommRow(t, st, pend.ID); n != 0 {
		t.Fatalf("pending leg must not write commission row: n=%d", n)
	}
}

// TestD013_PositionNetUSD_Replay498 ②：#498 实测数据回放 → +1.17。
func TestD013_PositionNetUSD_Replay498(t *testing.T) {
	st := sharedTestStore(t)
	ctx := context.Background()
	st.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_cashflows, broker_symbol_map RESTART IDENTITY CASCADE`)
	t.Cleanup(func() {
		st.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_cashflows, broker_symbol_map RESTART IDENTITY CASCADE`)
	})
	if _, err := st.Pool().Exec(ctx, `INSERT INTO broker_accounts
		(venue, host, port, password, venue_type) VALUES ('3001157','h',0,'','mt5')
		ON CONFLICT DO NOTHING`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO broker_symbol_map
		(venue, broker_symbol, canonical, contract_size) VALUES ('3001157','XAUUSDm','XAUUSD',100)`); err != nil {
		t.Fatal(err)
	}
	posID := seedD013Pos(t, st, "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp:d013b", "CLOSED")
	feeO, feeC := 2.18095, 2.1747
	d013Leg(t, st, posID, 0, "OPEN", "3001157:mt5", "BUY", 0.01, 4356.863, "FILLED", nil)
	d013Leg(t, st, posID, 1, "OPEN", "okx:perp", "SELL", 1, 4361.9, "FILLED", &feeO)
	d013Leg(t, st, posID, 0, "CLOSE", "3001157:mt5", "SELL", 0.01, 4344.498, "FILLED", nil)
	d013Leg(t, st, posID, 1, "CLOSE", "okx:perp", "BUY", 1, 4349.4, "FILLED", &feeC)
	// funding 实采行（#498 两条）
	for i, amt := range []float64{3.1341689447346255, 2.2583729292467316} {
		ek := "f:498:" + string(rune('a'+i))
		if _, err := st.InsertCashflowFull(ctx, &Cashflow{
			PositionID: posID, Kind: "funding", Venue: "okx", AmountUSD: amt,
			Ts: time.Now(), EventKey: &ek,
		}); err != nil {
			t.Fatal(err)
		}
	}
	net, err := st.PositionNetUSD(ctx, posID, nil)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(net-1.1769) > 0.01 {
		t.Fatalf("#498 replay net=%v want ≈1.1769", net)
	}
	// 腿键行铺位后旧 exec:* 键 commission 退出计算（留档不删、不双计）
	ek := "exec:498:1:commission:2026-09-23"
	if _, err := st.InsertCashflowFull(ctx, &Cashflow{
		PositionID: posID, Kind: "commission", Venue: "okx", AmountUSD: -9.99,
		Ts: time.Now(), EventKey: &ek,
	}); err != nil {
		t.Fatal(err)
	}
	net2, _ := st.PositionNetUSD(ctx, posID, nil)
	if math.Abs(net2-net) > 1e-9 {
		t.Fatalf("legacy exec:* commission must be ignored once leg: rows exist: %v vs %v", net2, net)
	}
	// override 分支：价差组件替换（exec 校准器权威值通道）
	override := 0.135
	net3, _ := st.PositionNetUSD(ctx, posID, &override)
	if want := override + 5.392541873981357 - 4.35565; math.Abs(net3-want) > 0.01 {
		t.Fatalf("override net=%v want ≈%v", net3, want)
	}
}

// TestD013_IncidentExclusion ③：事故族从聚合/日盈亏全出口排除。
func TestD013_IncidentExclusion(t *testing.T) {
	st := sharedTestStore(t)
	ctx := context.Background()
	st.Pool().Exec(ctx, `TRUNCATE positions, position_settlements RESTART IDENTITY CASCADE`)
	t.Cleanup(func() {
		st.Pool().Exec(ctx, `TRUNCATE positions, position_settlements RESTART IDENTITY CASCADE`)
	})
	seed := func(exitKind, statsStatus string, incidentAttr bool, net float64) int64 {
		id := seedD013Pos(t, st, "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp:"+exitKind, "CLOSED")
		if _, err := st.Pool().Exec(ctx, `UPDATE positions SET exit_kind=$1, closed_at=now(), realized_pnl=$2 WHERE id=$3`,
			exitKind, net, id); err != nil {
			t.Fatal(err)
		}
		if _, err := st.Pool().Exec(ctx, `INSERT INTO position_settlements
			(position_id, realized_usd, net_realized_usd, stats_status, incident_attributed, settled_at)
			VALUES ($1,$2,$2,$3,$4,now())`, id, net, statsStatus, incidentAttr); err != nil {
			t.Fatal(err)
		}
		return id
	}
	seed("other", "applied", false, 5)
	seed("incident_close", "applied", true, -2)
	seed("legged_unwind", "applied", true, -1)
	seed("other", "applied", true, -3) // incident_attributed 单判据也排
	seed("other", "skipped", false, 7) // skipped 本就不入样
	agg, err := st.AggregateStrategyStats(ctx, "XASSET_CARRY")
	if err != nil {
		t.Fatal(err)
	}
	if agg.Total != 1 || agg.AvgPnl != 5 {
		t.Fatalf("incident exclusion aggregate: %+v", agg)
	}
	agg2, err := st.AggregateStrategyStatsByEnvironment(ctx, "XASSET_CARRY", "live")
	if err != nil {
		t.Fatal(err)
	}
	if agg2.Total != 1 {
		t.Fatalf("env aggregate: %+v", agg2)
	}
	agg3, err := st.AggregateStrategyStatsByEnvironmentFamily(ctx, []string{"XASSET_CARRY"}, "live")
	if err != nil {
		t.Fatal(err)
	}
	if agg3.Total != 1 {
		t.Fatalf("family aggregate: %+v", agg3)
	}
	daily, err := st.DailyRealizedPnl(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(daily-12) > 1e-6 { // 5(other,net_realized)+7(skipped 行 fallback net_realized 也计日盈亏——口径保留)
		t.Fatalf("daily realized pnl: %v", daily)
	}
}

// TestD013_R1_ResolveLegFillCommission ①：UNKNOWN→FILLED 消解也必写腿键 commission 行。
func TestD013_R1_ResolveLegFillCommission(t *testing.T) {
	st := sharedTestStore(t)
	ctx := context.Background()
	st.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_cashflows RESTART IDENTITY CASCADE`)
	t.Cleanup(func() {
		st.Pool().Exec(ctx, `TRUNCATE positions, position_legs, position_cashflows RESTART IDENTITY CASCADE`)
	})
	posID := seedD013Pos(t, st, "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp:d013r1", "OPEN")
	cex := d013Leg(t, st, posID, 0, "OPEN", "okx:perp", "SELL", 1, 4300, "UNKNOWN", nil)
	mt := d013Leg(t, st, posID, 1, "OPEN", "3001157:mt5", "BUY", 0.01, 4300, "UNKNOWN", nil)
	if _, n := legCommRow(t, st, cex.ID); n != 0 {
		t.Fatalf("UNKNOWN leg must not have commission row: n=%d", n)
	}
	if err := st.ResolveLegFill(ctx, cex.ID, 4300.5, 1, 1.25); err != nil {
		t.Fatal(err)
	}
	if amt, n := legCommRow(t, st, cex.ID); n != 1 || amt != -1.25 {
		t.Fatalf("resolved CEX leg commission: n=%d amt=%v want -1.25", n, amt)
	}
	if err := st.ResolveLegFill(ctx, mt.ID, 4300.5, 0.01, 0); err != nil {
		t.Fatal(err)
	}
	if amt, n := legCommRow(t, st, mt.ID); n != 1 || amt != 0 {
		t.Fatalf("resolved MT leg commission: n=%d amt=%v want 0", n, amt)
	}
	// 幂等：重复消解不增生行
	if err := st.ResolveLegFill(ctx, cex.ID, 4300.5, 1, 1.25); err != nil {
		t.Fatal(err)
	}
	if _, n := legCommRow(t, st, cex.ID); n != 1 {
		t.Fatalf("idempotent resolve: n=%d", n)
	}
}
