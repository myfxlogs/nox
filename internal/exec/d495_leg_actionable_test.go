// d495_leg_actionable_test.go —— D-495 S7：腿可平性闸五场景。
// T1 单元 mock：键域分流+暗/亮/trade_mode 否决/冷启动单值（无 DSN）。
// T2-T4 DSN（ARB_EXEC_TEST_PG_DSN）：close gating 一暗一亮零发单不翻态 /
// ResumeClose 暗腿跳过幂等+残留降级 warn / force 覆盖可平。
package exec

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// --- T1：LegActionable 单元（mock store） ---

type d495FakeStore struct {
	changed    bool
	n          int
	err        error
	gotVenue   string
	gotSym     string
	canonMap   map[string]string // D-536：(venue+"|"+brokerSym)→canonical；nil/未命中=ok false
	canonCalls int
}

func (f *d495FakeStore) TicksPriceChanged(_ context.Context, venue, symbol string, _ time.Duration) (bool, int, error) {
	f.gotVenue, f.gotSym = venue, symbol
	return f.changed, f.n, f.err
}
func (f *d495FakeStore) CanonicalByBrokerSymbol(_ context.Context, venue, brokerSymbol string) (string, bool, error) {
	f.canonCalls++
	c, ok := f.canonMap[venue+"|"+brokerSymbol]
	return c, ok, nil
}

// d495FakeProber fake 执行时 trade_mode 即时探针（D-004 P1#2）。
type d495FakeProber struct {
	mode   int64
	err    error
	called bool
	gotSym string
}

func (p *d495FakeProber) TradeModeNow(_ context.Context, brokerSymbol string) (int64, error) {
	p.called = true
	p.gotSym = brokerSymbol
	return p.mode, p.err
}

func TestD495_LegActionable_KeyMapping(t *testing.T) {
	ctx := context.Background()

	// MT 腿：venueKey 剥到裸键，原生 symbol 直查
	mt := &d495FakeStore{changed: true, n: 3}
	ok, _ := LegActionable(ctx, mt, &d495FakeProber{mode: fact.TradeModeFull}, "3001157:mt5", "XAUUSDm", "BUY", true)
	if !ok {
		t.Fatal("亮腿应可行动")
	}
	if mt.gotVenue != "3001157" || mt.gotSym != "XAUUSDm" {
		t.Fatalf("MT 键域映射错: venue=%q symbol=%q", mt.gotVenue, mt.gotSym)
	}

	// CEX 腿：symbol 走 CexSymbolKey 归一（XAU-USDT-SWAP→XAUUSDT）
	// CEX 腿：无 trade_mode 概念，prober=nil 不探测照常判 ticks
	cex := &d495FakeStore{changed: true, n: 2}
	ok, _ = LegActionable(ctx, cex, nil, "okx:perp", "XAU-USDT-SWAP", "SELL", true)
	if !ok {
		t.Fatal("亮腿应可行动")
	}
	if cex.gotVenue != "okx" || cex.gotSym != "XAUUSDT" {
		t.Fatalf("CEX 键域映射错: venue=%q symbol=%q", cex.gotVenue, cex.gotSym)
	}

	// 暗腿（冻结/休市单值）→ 不可行动
	dark := &d495FakeStore{changed: false, n: 1}
	if ok, why := LegActionable(ctx, dark, &d495FakeProber{mode: fact.TradeModeFull}, "3001157:mt5", "XAUUSDm", "BUY", true); ok {
		t.Fatal("单值冻结腿应不可行动")
	} else if !strings.Contains(why, "无变化") {
		t.Fatalf("reason 应含窗口无变化语义: %s", why)
	}

	// trade_mode=DISABLED 否决（ticks 亮也拦；P1#2 改即时探针取值）
	dis := &d495FakeStore{changed: true, n: 5}
	if ok, why := LegActionable(ctx, dis, &d495FakeProber{mode: fact.TradeModeDisabled}, "3001157:mt5", "XAUUSDm", "SELL", true); ok {
		t.Fatal("trade_mode=disabled 应否决")
	} else if !strings.Contains(why, "disabled") {
		t.Fatalf("reason 应含 disabled: %s", why)
	}

	// store 查询失败 fail-closed
	bad := &d495FakeStore{err: fmt.Errorf("db down")}
	if ok, _ := LegActionable(ctx, bad, nil, "okx:perp", "XAU-USDT-SWAP", "BUY", true); ok {
		t.Fatal("tick 查询失败应 fail-closed")
	}
}

// TestD536_LegActionable_CanonicalTickKey D-536 键域死锁修复：MT 分支
// brokerSymbol→canonical 映射只作用于 TicksPriceChanged（ticks 写侧 canonical 键）。
func TestD536_LegActionable_CanonicalTickKey(t *testing.T) {
	ctx := context.Background()
	mk := func(changed bool, n int) *d495FakeStore {
		return &d495FakeStore{changed: changed, n: n,
			canonMap: map[string]string{"3001157|XAUUSDm": "XAUUSD"}}
	}

	// 主用例：XAUUSDm→XAUUSD 映射命中 → tick 查询键=canonical，亮腿放行。
	mt := mk(true, 29)
	prober := &d495FakeProber{mode: fact.TradeModeFull}
	ok, _ := LegActionable(ctx, mt, prober, "3001157:mt5", "XAUUSDm", "BUY", true)
	if !ok {
		t.Fatal("canonical 键命中+亮腿应可行动")
	}
	if mt.gotVenue != "3001157" || mt.gotSym != "XAUUSD" {
		t.Fatalf("tick 键应为 canonical: venue=%q symbol=%q", mt.gotVenue, mt.gotSym)
	}
	if prober.gotSym != "XAUUSDm" {
		t.Fatalf("探针键应保持原生名: %q", prober.gotSym)
	}

	// 对抗用例：映射在但 ticks 窗口 distinct<2 → 仍拦（键对而市场真关≠放行）。
	dark := mk(false, 1)
	if ok, _ := LegActionable(ctx, dark, &d495FakeProber{mode: fact.TradeModeFull}, "3001157:mt5", "XAUUSDm", "BUY", true); ok {
		t.Fatal("市场真关不应为放行而放行")
	}
	if dark.gotSym != "XAUUSD" {
		t.Fatalf("映射命中但 tick 键应仍 canonical: %q", dark.gotSym)
	}

	// 回归 a：映射未命中 → symbol 保持原生名，行为不变（照旧判死域）。
	nomap := &d495FakeStore{changed: true, n: 3, canonMap: map[string]string{}}
	ok, _ = LegActionable(ctx, nomap, &d495FakeProber{mode: fact.TradeModeFull}, "3001157:mt5", "XAUUSDm", "BUY", true)
	if !ok || nomap.gotSym != "XAUUSDm" {
		t.Fatalf("未命中应保持原生名: ok=%v sym=%q", ok, nomap.gotSym)
	}

	// 回归 b：CEX 分支仍走 CexSymbolKey，不调用 CanonicalByBrokerSymbol。
	cex := &d495FakeStore{changed: true, n: 2}
	ok, _ = LegActionable(ctx, cex, nil, "okx:perp", "BTC-USDT-SWAP", "BUY", true)
	if !ok || cex.gotSym != "BTCUSDT" {
		t.Fatalf("CEX 分支应走 CexSymbolKey: ok=%v sym=%q", ok, cex.gotSym)
	}
	if cex.canonCalls != 0 {
		t.Fatalf("CEX 分支不应调 CanonicalByBrokerSymbol: %d", cex.canonCalls)
	}
}

// --- T2-T4：DSN 端到端 ---

func d495SeedPos(t *testing.T, s *pgstore.Store, status string) int64 {
	t.Helper()
	ctx := context.Background()
	var posID int64
	if err := s.Pool().QueryRow(ctx, `
		INSERT INTO positions (opportunity_id, type, status, opened_at)
		VALUES ('PERP:XAUUSDT:okx:perp::d495', 'PERP_FUNDING', $1, NOW())
		RETURNING id`, status).Scan(&posID); err != nil {
		t.Fatalf("seed position: %v", err)
	}
	return posID
}

func d495SeedLeg(t *testing.T, s *pgstore.Store, posID int64, legNo int16, venue, symbol, side, orderID string) {
	t.Helper()
	if err := s.CreatePositionLeg(context.Background(), &pgstore.PositionLeg{
		PositionID: posID, LegNo: legNo, Phase: "OPEN", Venue: venue,
		Symbol: symbol, Side: side, Qty: 0.01, OrderID: orderID, Status: "FILLED",
	}); err != nil {
		t.Fatalf("seed leg: %v", err)
	}
}

func d495Pipeline(s *pgstore.Store, placeCount *int32, orders map[string][]OpenedOrder, drainAfter int) *Pipeline {
	reg := NewRegistry()
	reg.Register(&fakeFlattenExecutor{venue: "okx:perp", placeCount: placeCount})
	reg.Register(&fakeMT5CloseExecutor{venue: "3001157:mt5"})
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 100000, MaxLegImbalancePct: 50}, "demo")
	p.verifier = &fakeVerifier{drainAfter: drainAfter, orders: orders}
	return p
}

// fakeMT5CloseExecutor MT 腿 CloseOrder fake。
type fakeMT5CloseExecutor struct {
	venue      string
	placeCount *int32 // D-539：CloseOrder 调用计数（零发单断言）
}

func (e *fakeMT5CloseExecutor) Key() string { return e.venue }
func (e *fakeMT5CloseExecutor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	return Fill{OrderID: "mt-fill", Price: 100, Qty: o.Qty}, nil
}
func (e *fakeMT5CloseExecutor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	if e.placeCount != nil {
		atomic.AddInt32(e.placeCount, 1)
	}
	return Fill{OrderID: "mt-close-" + ref.OrderID, Price: 100, Qty: 0.01}, nil
}

// seedD495Ticks 给 okx 腿播两条不同 bid（亮）；MT 腿不播（暗）。
func seedD495Ticks(t *testing.T, s *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.EnsureMonthPartition(ctx, time.Now().Year(), int(time.Now().Month())); err != nil {
		t.Fatalf("partition: %v", err)
	}
	if err := s.InsertTicks(ctx, []pgstore.TickRow{
		{Ts: time.Now().Add(-2 * time.Minute), Venue: "okx", Symbol: "XAUUSDT", Bid: 4200.0, Ask: 4200.1, VenueType: "perp"},
		{Ts: time.Now(), Venue: "okx", Symbol: "XAUUSDT", Bid: 4200.5, Ask: 4200.6, VenueType: "perp"},
	}); err != nil {
		t.Fatalf("seed ticks: %v", err)
	}
}

// T2 一暗一亮 → ClosePosition 拒（ErrLegNotActionable）+ position 不翻态 + 零发单。
func TestD495_CloseGating_DarkLegBlocks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID := d495SeedPos(t, s, "OPEN")
	d495SeedLeg(t, s, posID, 0, "okx:perp", "XAU-USDT-SWAP", "SELL", "cex-open-1")
	d495SeedLeg(t, s, posID, 1, "d495mt:mt5", "XAUUSDm", "BUY", "mt-open-1")
	seedD495Ticks(t, s) // 只亮 CEX 腿，MT 腿暗

	var placed int32
	p := d495Pipeline(s, &placed, map[string][]OpenedOrder{}, 0)
	err := p.ClosePosition(ctx, posID)
	if !errors.Is(err, ErrLegNotActionable) {
		t.Fatalf("暗腿仓应 ErrLegNotActionable, got %v", err)
	}
	var lae *LegNotActionableError
	if !errors.As(err, &lae) || lae.Symbol != "XAUUSDm" {
		t.Fatalf("错误应携带暗腿 symbol: %+v", err)
	}
	pos, _ := s.GetPosition(ctx, posID)
	if pos.Status != "OPEN" {
		t.Fatalf("拒平后 position 应保持 OPEN, got %s", pos.Status)
	}
	if atomic.LoadInt32(&placed) != 0 {
		t.Fatal("拒平后不得有任何发单")
	}
}

// T3 ResumeClose 暗腿（D-539 改判）：全腿前置闸在腿循环前整仓拦截——
// 不写 CLOSE leg、零发单、ErrLegNotActionable 哨兵、note 合并 await_session
// （异类 token 不吞），二次调用幂等不漂移。残留降级 warn 锚由闸前返回不再触发。
func TestD495_ResumeClose_DarkLegAwaitSession(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID := d495SeedPos(t, s, "CLOSING")
	if err := s.SetPositionNote(ctx, posID, "settle_failed: keepme"); err != nil {
		t.Fatalf("seed note: %v", err)
	}
	d495SeedLeg(t, s, posID, 0, "okx:perp", "D495A-USDT-SWAP", "SELL", "cex-open-2")
	// 双腿全暗（无 ticks）→ 两腿均 await 跳过；verifier 报腿仍在仓
	orders := map[string][]OpenedOrder{
		"okx:perp": {{Symbol: "D495A-USDT-SWAP", Side: "SELL", PosSide: "short"}},
	}
	var placed int32
	p := d495Pipeline(s, &placed, orders, 0)

	err := p.ResumeClose(ctx, posID)
	if !errors.Is(err, ErrLegNotActionable) {
		t.Fatalf("暗腿仓 ResumeClose 应 ErrLegNotActionable, got %v", err)
	}
	if atomic.LoadInt32(&placed) != 0 {
		t.Fatal("暗腿不得发平仓单")
	}
	closeLegs, _ := s.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	if len(closeLegs) != 0 {
		t.Fatalf("暗腿不得写 CLOSE leg, got %d", len(closeLegs))
	}
	pos, _ := s.GetPosition(ctx, posID)
	if pos.Status != "CLOSING" {
		t.Fatalf("仓应保持 CLOSING 等下轮, got %s", pos.Status)
	}
	if !strings.Contains(pos.Note, "await_session:okx:perp:D495A-USDT-SWAP") {
		t.Fatalf("note 应含 await_session token: %q", pos.Note)
	}
	if !strings.Contains(pos.Note, "settle_failed: keepme") {
		t.Fatalf("note 合并写不得吞异类 token: %q", pos.Note)
	}
	// D-539：闸前返回不产生残留告警（观测面=note+哨兵日志）。
	var alertN int
	if err := s.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM alerts WHERE position_id=$1`, posID).Scan(&alertN); err != nil {
		t.Fatalf("alerts count: %v", err)
	}
	if alertN != 0 {
		t.Fatalf("前置闸轮不应产生告警, got %d", alertN)
	}
	// 幂等：第二次 ResumeClose 同哨兵返回、note 不变、仍无告警
	if err := p.ResumeClose(ctx, posID); !errors.Is(err, ErrLegNotActionable) {
		t.Fatalf("二次 ResumeClose 应仍 ErrLegNotActionable, got %v", err)
	}
	if err := s.Pool().QueryRow(ctx,
		`SELECT COUNT(*) FROM alerts WHERE position_id=$1`, posID).Scan(&alertN); err != nil {
		t.Fatalf("alerts recount: %v", err)
	}
	if alertN != 0 {
		t.Fatalf("幂等轮仍不应产生告警, total=%d", alertN)
	}
	pos2, _ := s.GetPosition(ctx, posID)
	if pos2.Note != pos.Note {
		t.Fatalf("note 幂等重写不得漂移: %q → %q", pos.Note, pos2.Note)
	}
}

// T4 force=true 逃逸门：暗腿仓强制平仓放行执行（CLOSE leg 落库）。
func TestD495_CloseForce_BypassesGate(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	posID := d495SeedPos(t, s, "OPEN")
	d495SeedLeg(t, s, posID, 0, "okx:perp", "D495B-USDT-SWAP", "SELL", "cex-open-3")
	// 不播 ticks → 腿暗；verifier drainAfter=1：preflight 报在仓、残核查报已平
	var placed int32
	p := d495Pipeline(s, &placed, map[string][]OpenedOrder{
		"okx:perp": {{Symbol: "D495B-USDT-SWAP", Side: "SELL", PosSide: "short"}},
	}, 1)

	if err := p.ClosePosition(ctx, posID); !errors.Is(err, ErrLegNotActionable) {
		t.Fatalf("force=false 应被闸拦, got %v", err)
	}
	if err := p.ClosePositionForce(ctx, posID); err != nil {
		t.Fatalf("force=true 应放行平仓, got %v", err)
	}
	pos, _ := s.GetPosition(ctx, posID)
	if pos.Status != "CLOSED" {
		t.Fatalf("force 平仓后应 CLOSED, got %s", pos.Status)
	}
	if atomic.LoadInt32(&placed) != 1 {
		t.Fatalf("force 应发 1 平仓单, got %d", placed)
	}
}
