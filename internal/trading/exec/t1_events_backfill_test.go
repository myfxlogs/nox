package exec

// d577_gate_backfill_test.go —— D-577 S5：CEX 平仓「超时但成交」历史回填锚（T1-T4）。
// 首选源=cex_private_events 落库帧 → miss → GateTradeHistory REST 兜底 → 双空 fail-closed。

import (
	"context"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/store/pgstore"
)

// d577Seed 播种 CLOSING position + gate:perp OPEN leg（XAU_USDT SELL 10 张，quanto=1）。
func d577Seed(t *testing.T, s *pgstore.Store, qty float64) (*pgstore.Position, pgstore.PositionLeg) {
	ctx := context.Background()
	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at, closing_started_at)
		VALUES ('XASSET:XAU:gate:perp:mt5:1', 'XASSET_CARRY', 'CLOSING', NOW()-interval '1 hour', NOW()-interval '10 minutes')
		RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	leg := pgstore.PositionLeg{
		PositionID: posID, LegNo: 0, Phase: "OPEN", Venue: "gate:perp",
		Symbol: "XAU_USDT", Side: "SELL", Qty: qty, OrderID: "T-d577", Status: "FILLED",
	}
	fq := qty
	leg.FilledQty = &fq
	if err := s.CreatePositionLeg(ctx, &leg); err != nil {
		t.Fatalf("seed leg: %v", err)
	}
	pos, err := s.GetPosition(ctx, posID)
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	return pos, leg
}

// d577Fixture gate executor+pipeline 夹具：spec 路由预注（quanto=1），其余 handlers 按注入。
func d577Fixture(t *testing.T, s *pgstore.Store, handlers map[string]func(http.ResponseWriter, *http.Request)) (*Pipeline, *d570Fixture) {
	handlers["/api/v4/futures/usdt/contracts/XAU_USDT"] = func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"quanto_multiplier":"1","order_size_min":1,"order_size_max":100000,"enable_decimal":false,"in_delisting":false,"leverage_max":"100","market_order_size_max":"100000","status":"trading"}`)
	}
	GateSpecInvalidate("XAU_USDT")
	t.Cleanup(func() { GateSpecInvalidate("XAU_USDT") }) // 包级 gateSpecCache 24h TTL——夹具规格出包须清，防下游用例吃进 poison
	f := newD570Fixture(t, handlers)
	reg := NewRegistry()
	reg.Register(f.e)
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	return p, f
}

func d577Truncate(t *testing.T, s *pgstore.Store) {
	if _, err := s.Pool().Exec(context.Background(),
		`TRUNCATE positions, position_legs, alerts, position_cashflows, cex_private_events RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
}

// TestD577_T1_EventsBackfill T1：usertrades×3+position_closes×1 落库 →
// VWAP ClosePrice、fee 合计 Commission、position_closes 最新条 pnl→Profit；CLOSE 腿回填。
func TestD577_T1_EventsBackfill(t *testing.T) {
	s := testStore(t)
	d577Truncate(t, s)
	ctx := context.Background()
	pos, leg := d577Seed(t, s, 10)
	// 事件帧（真形 payload 数组，create_time 均晚于 ClosingStartedAt）。
	for _, p := range []string{
		`[{"id":11,"contract":"XAU_USDT","size":4,"price":"100","fee":"-0.1","create_time":4102444900}]`,
		`[{"id":12,"contract":"XAU_USDT","size":3,"price":"101","fee":"-0.1","create_time":4102444901},{"id":13,"contract":"XAU_USDT","size":3,"price":"102","fee":"-0.1","create_time":4102444902}]`,
		`[{"id":99,"contract":"BTC_USDT","size":50,"price":"5","fee":"-9","create_time":4102444903}]`, // 异合约噪音不入聚合
	} {
		if err := s.InsertCEXPrivateEvent(ctx, "gate", "perp", "futures.usertrades", "", []byte(p)); err != nil {
			t.Fatalf("insert usertrades: %v", err)
		}
	}
	for _, p := range []string{
		`[{"contract":"XAU_USDT","side":"short","pnl":"-9.99","time":4102444800}]`, // 旧帧非最新
		`[{"contract":"XAU_USDT","side":"short","pnl":"-1.552255","time":4102444900}]`,
	} {
		if err := s.InsertCEXPrivateEvent(ctx, "gate", "perp", "futures.position_closes", "", []byte(p)); err != nil {
			t.Fatalf("insert position_closes: %v", err)
		}
	}
	p, f := d577Fixture(t, s, map[string]func(http.ResponseWriter, *http.Request){})
	// REST 兜底不得被触达（WS 帧已命中）——fixture 未挂路由，触达即 unexpected。
	hp, err := p.cexCloseFill(ctx, leg, pos.OpenedAt.Add(-time.Hour))
	if err != nil || hp == nil {
		t.Fatalf("cexCloseFill: hp=%v err=%v", hp, err)
	}
	// VWAP=(4×100+3×101+3×102)/10=100.9；fee=-0.3；pnl=最新条 -1.552255。
	if math.Abs(hp.ClosePrice-100.9) > 1e-9 {
		t.Fatalf("VWAP 不符: %v", hp.ClosePrice)
	}
	if math.Abs(hp.Commission-(-0.3)) > 1e-9 {
		t.Fatalf("fee 合计不符: %v", hp.Commission)
	}
	if hp.Profit != -1.552255 {
		t.Fatalf("pnl 不符: %v", hp.Profit)
	}
	// 完整回填路径（since=ClosingStartedAt 窗内）。
	if err := p.backfillCloseFromHistory(ctx, pos, leg, 1.0); err != nil {
		t.Fatalf("backfill: %v", err)
	}
	legs, _ := s.ListPositionLegs(ctx, pos.ID)
	var closeLeg *pgstore.PositionLeg
	for i := range legs {
		if legs[i].Phase == "CLOSE" {
			closeLeg = &legs[i]
		}
	}
	if closeLeg == nil || closeLeg.PriceActual == nil || math.Abs(*closeLeg.PriceActual-100.9) > 1e-9 {
		t.Fatalf("CLOSE 腿价不符: %+v", closeLeg)
	}
	if closeLeg.Side != "BUY" || closeLeg.Status != "FILLED" {
		t.Fatalf("CLOSE 腿向/态不符: %+v", closeLeg)
	}
	if f.orderCalls.Load() != 0 {
		t.Fatalf("回填路径禁发新单, orderCalls=%d", f.orderCalls.Load())
	}
}

// TestD577_T2_RESTFallback T2：WS 帧零命中 → GateTradeHistory REST 兜底同口径回填。
func TestD577_T2_RESTFallback(t *testing.T) {
	s := testStore(t)
	d577Truncate(t, s)
	ctx := context.Background()
	pos, leg := d577Seed(t, s, 10)
	h := map[string]func(http.ResponseWriter, *http.Request){
		"/api/v4/futures/usdt/my_trades": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[
				{"contract":"XAU_USDT","size":6,"price":"4160","fee":"-0.5","create_time":4102444900},
				{"contract":"XAU_USDT","size":4,"price":"4162","fee":"-0.3","create_time":4102444901},
				{"contract":"XAU_USDT","size":99,"price":"1","fee":"-9","create_time":1000}]`) // 旧成交出窗
		},
		"/api/v4/futures/usdt/position_close": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[{"contract":"XAU_USDT","side":"short","pnl":"-1.552255","time":4102444900}]`)
		},
	}
	p, _ := d577Fixture(t, s, h)
	hp, err := p.cexCloseFill(ctx, leg, pos.ClosingStartedAt.Add(-time.Second))
	if err != nil || hp == nil {
		t.Fatalf("cexCloseFill REST: hp=%v err=%v", hp, err)
	}
	// VWAP=(6×4160+4×4162)/10=4160.8；fee=-0.8。
	if math.Abs(hp.ClosePrice-4160.8) > 1e-9 {
		t.Fatalf("REST VWAP 不符: %v", hp.ClosePrice)
	}
	if math.Abs(hp.Commission-(-0.8)) > 1e-9 {
		t.Fatalf("REST fee 不符: %v", hp.Commission)
	}
	if hp.Profit != -1.552255 {
		t.Fatalf("REST pnl 不符: %v", hp.Profit)
	}
	if err := p.backfillCloseFromHistory(ctx, pos, leg, 1.0); err != nil {
		t.Fatalf("backfill REST: %v", err)
	}
}

// TestD577_T3_BothEmpty T3：双源皆空 → no authoritative history，不写 CLOSE 腿+critical alert。
func TestD577_T3_BothEmpty(t *testing.T) {
	s := testStore(t)
	d577Truncate(t, s)
	ctx := context.Background()
	pos, leg := d577Seed(t, s, 10)
	h := map[string]func(http.ResponseWriter, *http.Request){
		"/api/v4/futures/usdt/my_trades": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[]`)
		},
		"/api/v4/futures/usdt/position_close": func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.WriteString(w, `[]`)
		},
	}
	p, _ := d577Fixture(t, s, h)
	err := p.backfillCloseFromHistory(ctx, pos, leg, 1.0)
	if err == nil || !strings.Contains(err.Error(), "no authoritative history") {
		t.Fatalf("期望 fail-closed 出口, got %v", err)
	}
	legs, _ := s.ListPositionLegs(ctx, pos.ID)
	for _, l := range legs {
		if l.Phase == "CLOSE" {
			t.Fatalf("双空不得写 CLOSE 腿: %+v", l)
		}
	}
}

// TestD577_T4_QtyMismatch T4：聚合量 < leg.EffectiveQty（半成交）→ fail-closed error。
func TestD577_T4_QtyMismatch(t *testing.T) {
	s := testStore(t)
	d577Truncate(t, s)
	ctx := context.Background()
	pos, leg := d577Seed(t, s, 10)
	if err := s.InsertCEXPrivateEvent(ctx, "gate", "perp", "futures.usertrades", "", []byte(
		`[{"id":11,"contract":"XAU_USDT","size":5,"price":"100","fee":"-0.1","create_time":4102444900}]`)); err != nil {
		t.Fatalf("insert: %v", err)
	}
	p, _ := d577Fixture(t, s, map[string]func(http.ResponseWriter, *http.Request){})
	err := p.backfillCloseFromHistory(ctx, pos, leg, 1.0)
	if err == nil {
		t.Fatal("半成交聚合须 fail-closed")
	}
	legs, _ := s.ListPositionLegs(ctx, pos.ID)
	for _, l := range legs {
		if l.Phase == "CLOSE" {
			t.Fatalf("污染量不得写 CLOSE 腿: %+v", l)
		}
	}
}
