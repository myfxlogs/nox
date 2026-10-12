// d018_maker_test.go —— D-018 阶段a 验收（DSN）：
// T1 首腿 post-only 超时 T → 撤单终查、零成交 → 全腿 FAILED、次腿零触达（零敞口）；
// T2 首腿成交回报 → 次腿 taker → OPEN，fill_kind maker/taker 落库；
// T3 首腿成交 + 次腿败 → LEGGED → 既有 all-CEX auto-flatten unwind（CLOSED，反向单出证）；
// T4 每 opp 每小时 ≤N 预算用尽 → fail-closed 拒执行（不回落市价）。
// posType=PERP_FUNDING（d127 先例：绕过 xasset allowlist/cap guard 的 opportunity 依赖，
// maker 序只看腿形状不看 posType，语义等价适格组合 spot×perp）。
package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/data/bus"
	"arb/internal/data/fact"
)

// fakeMakerExecutor PostOnlyPlacer 可脚本化 fake。
// fillAtAwait ≥1 时：第 fillAtAwait 次 AwaitPostOnlyFill 立即 filled；否则等满 ctx（超时形态）。
type fakeMakerExecutor struct {
	venue            string
	awaitCalls       int32
	fillAtAwait      int32
	cancelCalls      int32
	cancelFillQty    float64
	marketCalls      int32
	lastMarket       Order
	failMarketOnSide string // 命中侧市价单败（非超时错误=确定未成交→FAILED）
}

func (e *fakeMakerExecutor) Key() string { return e.venue }

func (e *fakeMakerExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, nil
}

func (e *fakeMakerExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	atomic.AddInt32(&e.marketCalls, 1)
	e.lastMarket = o
	if o.Side == e.failMarketOnSide {
		return Fill{}, fmt.Errorf("fake -1111 precision error (side=%s)", o.Side)
	}
	return Fill{OrderID: "mkt-" + o.Side, Price: 100.2, Qty: o.Qty, FeeUSD: 0.02}, nil
}

func (e *fakeMakerExecutor) PlacePostOnly(ctx context.Context, o Order, px float64) (string, error) {
	return "po-1", nil
}

func (e *fakeMakerExecutor) AwaitPostOnlyFill(ctx context.Context, o Order, orderID string) (Fill, bool, error) {
	n := atomic.AddInt32(&e.awaitCalls, 1)
	if e.fillAtAwait > 0 && n >= e.fillAtAwait {
		return Fill{OrderID: orderID, Price: 100.0, Qty: o.Qty, FeeUSD: 0.001}, true, nil
	}
	<-ctx.Done() // post-only 在挂等满超时 T（不被成交形态）
	return Fill{}, false, nil
}

func (e *fakeMakerExecutor) PostOnlyCancel(ctx context.Context, o Order, orderID string) (Fill, error) {
	atomic.AddInt32(&e.cancelCalls, 1)
	return Fill{OrderID: orderID, Qty: e.cancelFillQty, Price: 100.0}, nil
}

// d018MakerSetup maker 序测试管线：gate:perp=maker 腿 + okx:spot=次腿 taker；
// qbus 播 gate:perp 对手价（SELL 腿挂 ask）。
func d018MakerSetup(t *testing.T, perp, spot *fakeMakerExecutor, timeout time.Duration, maxPerHour int) *Pipeline {
	t.Helper()
	reg := NewRegistry()
	reg.Register(perp)
	reg.Register(spot)
	q := bus.New()
	q.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", VenueSymbol: "BTC_USDT", Bid: 100.0, Ask: 100.2, Time: time.Now()})
	p := NewPipeline(reg, testStore(t), q, RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 1_000_000}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	p.SetMakerPolicy(timeout, maxPerHour)
	return p
}

var d018Legs = []LegSpec{
	{VenueKey: "gate:perp", VenueSymbol: "BTC_USDT", Side: "SELL", Qty: 1, PriceExpected: 100.0},
	{VenueKey: "okx:spot", VenueSymbol: "BTC-USDT", Side: "BUY", Qty: 1, PriceExpected: 100.2},
}

// T1 超时撤单零敞口：post-only 挂满 T 未成交 → 撤单终查 qty=0 → 全腿 FAILED、次腿零触达。
func TestD018_MakerTimeoutCancelZeroExposure(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	perp := &fakeMakerExecutor{venue: "gate:perp"} // fillAtAwait=0 永不成交
	spot := &fakeMakerExecutor{venue: "okx:spot"}
	p := d018MakerSetup(t, perp, spot, 120*time.Millisecond, 10)

	res, err := p.Execute(ctx, "PERP:BTCUSDT:okx:spot:gate:perp:1", "req-d018-t1", "PERP_FUNDING", d018Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "FAILED" {
		t.Fatalf("超时干净撤应全腿 FAILED（零敞口），got %s", res.Status)
	}
	if perp.cancelCalls != 1 {
		t.Fatalf("超时后必须撤单终查，cancel=%d", perp.cancelCalls)
	}
	if spot.marketCalls != 0 {
		t.Fatalf("首腿未成交次腿不得发单（零持仓），market=%d", spot.marketCalls)
	}
	legs, err := s.ListPositionLegs(ctx, res.PositionID)
	if err != nil {
		t.Fatalf("list legs: %v", err)
	}
	if len(legs) != 2 {
		t.Fatalf("legs=%d, want 2（FAILED 腿也落库审计）", len(legs))
	}
	for _, l := range legs {
		// maker 腿=FAILED（干净撤=确定未成交）；次腿=PENDING（未尝试，零触达审计）。
		want := "PENDING"
		if l.LegNo == 0 {
			want = "FAILED"
		}
		if l.Status != want {
			t.Fatalf("leg %d status=%s, want %s", l.LegNo, l.Status, want)
		}
		if l.FillKind != nil {
			t.Fatalf("leg %d 未成交不得带 fill_kind，got %s", l.LegNo, *l.FillKind)
		}
	}
	pos, err := s.GetPosition(ctx, res.PositionID)
	if err != nil {
		t.Fatalf("get position: %v", err)
	}
	if pos.Status != "FAILED" {
		t.Fatalf("position status=%s, want FAILED（零敞口）", pos.Status)
	}
}

// T2 成交回报→次腿 taker：首腿立即成交 → 次腿发市价 → OPEN；fill_kind maker/taker 落库。
func TestD018_MakerFillThenTakerOpen(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	perp := &fakeMakerExecutor{venue: "gate:perp", fillAtAwait: 1}
	spot := &fakeMakerExecutor{venue: "okx:spot"}
	p := d018MakerSetup(t, perp, spot, 2*time.Second, 10)

	res, err := p.Execute(ctx, "PERP:BTCUSDT:okx:spot:gate:perp:2", "req-d018-t2", "PERP_FUNDING", d018Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("双腿成交应 OPEN，got %s", res.Status)
	}
	if spot.marketCalls != 1 {
		t.Fatalf("首腿成交后次腿必须 taker 发单，market=%d", spot.marketCalls)
	}
	if spot.lastMarket.Side != "BUY" {
		t.Fatalf("次腿方向=%s, want BUY（原腿方向）", spot.lastMarket.Side)
	}
	legs, err := s.ListPositionLegs(ctx, res.PositionID)
	if err != nil {
		t.Fatalf("list legs: %v", err)
	}
	type legSnap struct {
		status   string
		fillKind *string
	}
	byNo := map[int16]legSnap{}
	for _, l := range legs {
		byNo[l.LegNo] = legSnap{status: l.Status, fillKind: l.FillKind}
	}
	if byNo[0].status != "FILLED" || byNo[0].fillKind == nil || *byNo[0].fillKind != "maker" {
		t.Fatalf("腿0 应 FILLED/maker，got %+v", byNo[0])
	}
	if byNo[1].status != "FILLED" || byNo[1].fillKind == nil || *byNo[1].fillKind != "taker" {
		t.Fatalf("腿1 应 FILLED/taker，got %+v", byNo[1])
	}
}

// T3 首腿成交+次腿败（确定未成交）→ LEGGED → 既有 all-CEX auto-flatten unwind（CLOSED）。
func TestD018_MakerSecondLegFailUnwind(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	perp := &fakeMakerExecutor{venue: "gate:perp", fillAtAwait: 1}
	spot := &fakeMakerExecutor{venue: "okx:spot", failMarketOnSide: "BUY"} // 次腿败
	p := d018MakerSetup(t, perp, spot, 2*time.Second, 10)
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{"gate:perp": {}, "okx:spot": {}}}

	res, err := p.Execute(ctx, "PERP:BTCUSDT:okx:spot:gate:perp:3", "req-d018-t3", "PERP_FUNDING", d018Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "CLOSED" {
		t.Fatalf("首腿成交次腿败应走既有 unwind 收口 CLOSED，got %s", res.Status)
	}
	if spot.marketCalls != 1 {
		t.Fatalf("次腿应被尝试（败也是出证），market=%d", spot.marketCalls)
	}
	// unwind 反向单经 perp 执行器 PlaceOrder：BUY 平 SELL 腿（maker 腿市价计 1 次）
	if perp.marketCalls != 1 {
		t.Fatalf("收平反向单应发 1 次，got %d", perp.marketCalls)
	}
	if perp.lastMarket.Side != "BUY" {
		t.Fatalf("收平反向单应 BUY（平 SELL 腿），got %+v", perp.lastMarket)
	}
}

// T4 每 opp 每小时 ≤N：预算用尽 → fail-closed 拒执行（不回落市价，次腿零触达）。
func TestD018_MakerBudgetExhausted(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	perp := &fakeMakerExecutor{venue: "gate:perp"} // 永不成交=每次 Execute 消耗 1 预算
	spot := &fakeMakerExecutor{venue: "okx:spot"}
	p := d018MakerSetup(t, perp, spot, 80*time.Millisecond, 1)

	opp := "PERP:BTCUSDT:okx:spot:gate:perp:4"
	if _, err := p.Execute(ctx, opp, "req-d018-t4a", "PERP_FUNDING", d018Legs); err != nil {
		t.Fatalf("第一次 Execute（预算内）: %v", err)
	}
	_, err := p.Execute(ctx, opp, "req-d018-t4b", "PERP_FUNDING", d018Legs)
	if err == nil || !strings.Contains(err.Error(), "maker attempt budget exhausted") {
		t.Fatalf("预算用尽应拒执行，got %v", err)
	}
	if !errors.Is(err, ErrMakerBudgetExhausted) && !strings.Contains(err.Error(), ErrMakerBudgetExhausted.Error()) {
		t.Fatalf("应带哨兵错误，got %v", err)
	}
	if spot.marketCalls != 0 {
		t.Fatalf("预算用尽不得回落市价，market=%d", spot.marketCalls)
	}
}

// TestD018_MakerPlanEligibility 适格性判定：未配置 fail-closed / 双 perp 无 funding fact
// 回落既有（不启用）/ 非 perp 组合不启用 / 恰一 perp 适格（MT×perp 与 spot×perp 同形）。
func TestD018_MakerPlanEligibility(t *testing.T) {
	ctx := context.Background()
	p := &Pipeline{}
	if _, ok := p.makerPlan(ctx, "PERP:BTCUSDT:okx:spot:gate:perp:x", d018Legs); ok {
		t.Fatal("策略未配置（timeout/max=0）必须 fail-closed 不启用")
	}
	p.SetMakerPolicy(time.Second, 5)
	if _, ok := p.makerPlan(ctx, "SWAPARB:BTCUSDT:okx:perp:binance:perp:x", []LegSpec{
		{VenueKey: "okx:perp"}, {VenueKey: "binance:perp"},
	}); ok {
		t.Fatal("双 perp 无 funding fact（无 store）应回落既有市价路径")
	}
	if _, ok := p.makerPlan(ctx, "PERP:BTCUSDT:okx:spot:binance:spot:x", []LegSpec{
		{VenueKey: "okx:spot"}, {VenueKey: "binance:spot"},
	}); ok {
		t.Fatal("无 perp 腿不适格")
	}
	if plan, ok := p.makerPlan(ctx, "PERP:BTCUSDT:1279967:mt5:okx:perp:x", []LegSpec{
		{VenueKey: "1279967:mt5"}, {VenueKey: "okx:perp"},
	}); !ok || plan.makerIdx != 1 {
		t.Fatalf("MT×perp 应适格 maker=perp 腿(idx=1)，got ok=%v plan=%+v", ok, plan)
	}
	if plan, ok := p.makerPlan(ctx, "PERP:BTCUSDT:gate:perp:okx:spot:x", d018Legs); !ok || plan.makerIdx != 0 {
		t.Fatalf("spot×perp 应适格 maker=perp 腿(idx=0)，got ok=%v plan=%+v", ok, plan)
	}
}

// TestD018_SwapArbMakerLegByFunding 后段②：双 perp（SWAPARB）maker 腿=funding 净收
// 更高一侧。gate funding +10%/yr → SELL 腿 carry +10；okx funding −20%/yr → BUY 腿
// carry +20（多头收负费率）→ maker=okx 腿(idx1)。gate 改 −5 → SELL 腿 carry −5 <+20 不变；
// 再验反向：okx +30 → BUY carry −30 <gate +10 → maker=gate 腿(idx0)。
// funding fact 缺 → 不适格回落既有市价。
func TestD018_SwapArbMakerLegByFunding(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, opportunities, alerts, facts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	now := time.Now()
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: "gate", Symbol: "BTCUSDT", Value: 10.0, Unit: fact.UnitPctAnnualized, Ts: now, Src: "gate"},
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "BTCUSDT", Value: -20.0, Unit: fact.UnitPctAnnualized, Ts: now, Src: "okx"},
	}); err != nil {
		t.Fatalf("insert facts: %v", err)
	}
	legs := []LegSpec{
		{VenueKey: "gate:perp", VenueSymbol: "BTC_USDT", Side: "SELL", Qty: 1, PriceExpected: 100.0},
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "BUY", Qty: 1, PriceExpected: 100.2},
	}
	p := &Pipeline{store: s}
	p.SetMakerPolicy(time.Second, 5)
	opp := "SWAPARB:BTCUSDT:okx:perp:gate:perp:d018-mk"
	if plan, ok := p.makerPlan(ctx, opp, legs); !ok || plan.makerIdx != 1 {
		t.Fatalf("okx funding −20 → BUY 腿 carry +20 更优应挂 maker(idx=1)，got ok=%v plan=%+v", ok, plan)
	}
	// 反向：okx funding 正且高于 gate → BUY 腿付 funding 劣，SELL 腿挂 maker。
	if err := s.InsertFacts(ctx, []fact.Fact{
		{Kind: fact.KindFunding, Venue: "okx", Symbol: "BTCUSDT", Value: 30.0, Unit: fact.UnitPctAnnualized, Ts: now.Add(time.Second), Src: "okx"},
	}); err != nil {
		t.Fatalf("insert facts2: %v", err)
	}
	if plan, ok := p.makerPlan(ctx, opp, legs); !ok || plan.makerIdx != 0 {
		t.Fatalf("okx funding +30 → BUY 腿付 30 劣于 gate SELL +10，maker 应 idx=0，got ok=%v plan=%+v", ok, plan)
	}
	// funding fact 缺一腿 → 不启用（既有路径）。
	if _, err := s.Pool().Exec(ctx, `DELETE FROM facts WHERE venue='okx'`); err != nil {
		t.Fatalf("del fact: %v", err)
	}
	if _, ok := p.makerPlan(ctx, opp, legs); ok {
		t.Fatal("okx funding fact 缺应回落既有市价路径（ok=false）")
	}
}
