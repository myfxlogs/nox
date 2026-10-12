// d090_close_mt_first_test.go —— D-090 锚：平仓侧 MT 先行（对称 D-084 开仓侧）。
// (a) MT CloseOrder 拒单（10011）→ CEX 腿零下单、无 CLOSE leg、仓保持 CLOSING 非 LEGGED
//
//	（#498 型真 LEGGED 防线）；
//
// (b) MT=leg1 的仓排序后 MT 仍先发（leg_no 序不保证 MT 在前）；双腿全成 → CLOSED。
package exec

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

// d090SeqExec 平仓序执行器：CloseOrder（MT 腿）/PlaceOrder（CEX 腿）调用序入 seq；
// failClose=true 时 CloseOrder 返 10011 拒单。
type d090SeqExec struct {
	venue     string
	failClose bool
	mu        *sync.Mutex
	seq       *[]string
}

func (e *d090SeqExec) Key() string                                         { return e.venue }
func (e *d090SeqExec) TradeModeNow(context.Context, string) (int64, error) { return 4, nil }
func (e *d090SeqExec) PlaceOrder(_ context.Context, o Order) (Fill, error) {
	e.mu.Lock()
	*e.seq = append(*e.seq, "place:"+e.venue)
	e.mu.Unlock()
	return Fill{OrderID: "d090-place-" + e.venue, Price: 100, Qty: o.Qty}, nil
}
func (e *d090SeqExec) CloseOrder(_ context.Context, ref OrderRef) (Fill, error) {
	e.mu.Lock()
	*e.seq = append(*e.seq, "close:"+e.venue)
	e.mu.Unlock()
	if e.failClose {
		return Fill{}, fmt.Errorf("mt5 orderclose: [code=10011] REQUEST_ERROR")
	}
	return Fill{OrderID: ref.OrderID, Price: 100, Qty: 0.01}, nil
}

// seedD090Ticks 双腿亮（MT 原生名键 + canonical 键都播，CanonicalByBrokerSymbol
// 命中与否都可行动；CEX 归一键）。
func seedD090Ticks(t *testing.T, s *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	if err := s.EnsureMonthPartition(ctx, time.Now().Year(), int(time.Now().Month())); err != nil {
		t.Fatalf("partition: %v", err)
	}
	rows := []pgstore.TickRow{
		{Ts: time.Now().Add(-2 * time.Minute), Venue: "3001157", Symbol: "XAUUSDm", Bid: 4200.0, Ask: 4200.1, VenueType: "mt5"},
		{Ts: time.Now(), Venue: "3001157", Symbol: "XAUUSDm", Bid: 4200.5, Ask: 4200.6, VenueType: "mt5"},
		{Ts: time.Now().Add(-2 * time.Minute), Venue: "3001157", Symbol: "XAUUSD", Bid: 4200.0, Ask: 4200.1, VenueType: "mt5"},
		{Ts: time.Now(), Venue: "3001157", Symbol: "XAUUSD", Bid: 4200.5, Ask: 4200.6, VenueType: "mt5"},
		{Ts: time.Now().Add(-2 * time.Minute), Venue: "okx", Symbol: "XAUUSDT", Bid: 4200.0, Ask: 4200.1, VenueType: "perp"},
		{Ts: time.Now(), Venue: "okx", Symbol: "XAUUSDT", Bid: 4200.5, Ask: 4200.6, VenueType: "perp"},
	}
	if err := s.InsertTicks(ctx, rows); err != nil {
		t.Fatalf("seed ticks: %v", err)
	}
}

// d090Pipeline CEX=leg0 / MT=leg1 双腿 CLOSING 仓（顺序锚强形态：leg_no 序 CEX 在前）。
func d090Pipeline(t *testing.T, s *pgstore.Store, mt, cex *d090SeqExec) (*Pipeline, int64) {
	t.Helper()
	posID := d495SeedPos(t, s, "CLOSING")
	d495SeedLeg(t, s, posID, 0, "okx:perp", "XAU-USDT-SWAP", "SELL", "cex-open-1")
	d495SeedLeg(t, s, posID, 1, "3001157:mt5", "XAUUSDm", "BUY", "mt-open-1")
	seedD090Ticks(t, s)
	reg := NewRegistry()
	reg.Register(cex)
	reg.Register(mt)
	p := NewPipeline(reg, s, bus.New(), RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 1e6}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	return p, posID
}

// (a) MT 拒单 → CEX 腿零下单、仓保持 CLOSING（非 LEGGED）、无 CLOSE leg、error 透出（重试面）。
func TestD090_MTRejectCEXNotSent(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	mu, seq := &sync.Mutex{}, &[]string{}
	p, posID := d090Pipeline(t, s,
		&d090SeqExec{venue: "3001157:mt5", failClose: true, mu: mu, seq: seq},
		&d090SeqExec{venue: "okx:perp", mu: mu, seq: seq})
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"3001157:mt5": {{Ticket: "mt-open-1"}},
		"okx:perp":    {{Symbol: "XAU-USDT-SWAP", Side: "SELL"}},
	}}

	err := p.ResumeClose(ctx, posID)
	if err == nil || !strings.Contains(err.Error(), "MT leg close rejected") {
		t.Fatalf("MT 拒单轮应透出中断 error, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, s := range *seq {
		if strings.HasPrefix(s, "place:") {
			t.Fatalf("MT 拒单后 CEX 腿应零下单, seq=%v", *seq)
		}
	}
	if len(*seq) != 1 || (*seq)[0] != "close:3001157:mt5" {
		t.Fatalf("本轮应只触达 MT 腿, seq=%v", *seq)
	}
	pos, _ := s.GetPosition(ctx, posID)
	if pos.Status != "CLOSING" {
		t.Fatalf("仓应保持 CLOSING 等重试（非 LEGGED）, got %s", pos.Status)
	}
	closeLegs, lerr := s.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	// 败腿落行语义取代——FAILED 尝试行须存在（attempt_no=1+拒因原文），
	// 但不得有 FILLED 行（不假装平仓，去重键只认 FILLED 不阻下轮重试）。
	if lerr != nil {
		t.Fatal(lerr)
	}
	var filledN int
	for _, l := range closeLegs {
		if l.Status == "FILLED" {
			filledN++
		}
	}
	if filledN != 0 || len(closeLegs) != 1 || closeLegs[0].Status != "FAILED" {
		t.Fatalf("拒单轮应有恰一 FAILED 行零 FILLED 行: n=%d %+v", len(closeLegs), closeLegs)
	}
	if closeLegs[0].FailReason == nil || closeLegs[0].SubmittedAt == nil {
		t.Fatalf("败腿缺 fail_reason/submitted_at: %+v", closeLegs[0])
	}
}

// (b) 顺序锚：MT=leg1 排序后仍先发；双腿全成 → CLOSED（CEX=leg0 不再先平）。
func TestD090_CloseOrderMTFirst(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	mu, seq := &sync.Mutex{}, &[]string{}
	p, posID := d090Pipeline(t, s,
		&d090SeqExec{venue: "3001157:mt5", mu: mu, seq: seq},
		&d090SeqExec{venue: "okx:perp", mu: mu, seq: seq})
	// drainAfter=1：preflight（第 1 次）报在仓，残留校验（第 2 次起）报空。
	p.verifier = &fakeVerifier{drainAfter: 1, orders: map[string][]OpenedOrder{
		"3001157:mt5": {{Ticket: "mt-open-1"}},
		"okx:perp":    {{Symbol: "XAU-USDT-SWAP", Side: "SELL"}},
	}}

	if err := p.ResumeClose(ctx, posID); err != nil {
		t.Fatalf("双腿全成应 CLOSED 路径, got %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(*seq) != 2 || (*seq)[0] != "close:3001157:mt5" || (*seq)[1] != "place:okx:perp" {
		t.Fatalf("平仓序应 MT 先 CEX 后（MT=leg1 也然）, seq=%v", *seq)
	}
	pos, _ := s.GetPosition(ctx, posID)
	if pos.Status != "CLOSED" {
		t.Fatalf("双腿全成仓应 CLOSED, got %s", pos.Status)
	}
}

// (c) force 逃逸门豁免中断（D-090 R1）：MT 拒单后 CEX 腿仍下单——D-495 语义
// 「明知单腿裸露也要平」，中断只在非 force 路径生效；否则 MT 10018/10011
// 会把 CEX 逃逸腿永远堵死（pos502 形态）。
func TestD090_ForceBypassesMTRejectInterrupt(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts, position_cashflows RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	mu, seq := &sync.Mutex{}, &[]string{}
	p, posID := d090Pipeline(t, s,
		&d090SeqExec{venue: "3001157:mt5", failClose: true, mu: mu, seq: seq},
		&d090SeqExec{venue: "okx:perp", mu: mu, seq: seq})
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{
		"3001157:mt5": {{Ticket: "mt-open-1"}},
		"okx:perp":    {{Symbol: "XAU-USDT-SWAP", Side: "SELL"}},
	}}

	// force=true 直调核心（不走 ClosePositionForce 避 claim 状态耦合）；
	// MT 腿残留时尾部残留校验可能返 error——既有语义不断言，本锚只钉执行序与 CLOSE leg。
	_ = p.closePositionCore(ctx, posID, true)
	mu.Lock()
	got := append([]string(nil), *seq...)
	mu.Unlock()
	if len(got) != 2 || got[0] != "close:3001157:mt5" || got[1] != "place:okx:perp" {
		t.Fatalf("force 路径 MT 拒单后 CEX 仍应下单, seq=%v", got)
	}
	closeLegs, lerr := s.ListPositionLegsByPhase(ctx, posID, "CLOSE")
	if lerr != nil {
		t.Fatalf("list close legs: %v", lerr)
	}
	for _, l := range closeLegs {
		if l.Venue == "okx:perp" {
			return
		}
	}
	t.Fatalf("force 逃逸应落 okx:perp CLOSE leg, got %+v", closeLegs)
}
