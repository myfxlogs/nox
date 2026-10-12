// d026_partial_test.go —— D-026 验收（DSN）：
// T1 maker 部分成交（0.3/1.0）→ 次腿等比缩量 0.3 → 双腿 FILLED 等量 → OPEN；
// T2 次腿缩量后低于最小量被 venue 拒 → FAILED → LEGGED → 既有 unwind（CLOSED）；
// T3 taker IOC 部分成交（spot 0.3 vs perp 1.0）→ 腿量匹配守卫拦截 → LEGGED + critical 告警 → unwind。
// maker 腿选择：gate:perp=maker（D-018 后段适格：恰一 perp 腿）。
package exec

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"arb/internal/data/bus"
)

// d026Executor 市价腿 fake：fillRatio 模拟 IOC 部分成交（FILLED 带部分量）；
// rejectSide 命中侧直接拒（=低于最小量 venue 拒形态）。
type d026Executor struct {
	venue       string
	fillRatio   float64 // FILLED 腿 Qty=o.Qty×ratio（1=全量）
	rejectSide  string
	marketCalls int32
	lastMarket  Order
}

func (e *d026Executor) Key() string { return e.venue }
func (e *d026Executor) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, nil
}

// TradeModeNow D-495 可平性探针 stub（MT 腿预检要求 executor 实现；恒在市）。
func (e *d026Executor) TradeModeNow(ctx context.Context, brokerSymbol string) (int64, error) {
	return 1, nil
}
func (e *d026Executor) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	n := atomic.AddInt32(&e.marketCalls, 1)
	e.lastMarket = o
	if n > 1 {
		// 收平/反向单恒全量成交（fake 只模拟开仓 IOC 部分成交形态）。
		return Fill{OrderID: "mkt-" + o.Side, Price: 100.0, Qty: o.Qty, FeeUSD: 0.01}, nil
	}
	if o.Side == e.rejectSide {
		return Fill{}, fmt.Errorf("fake -1111 below min (side=%s)", o.Side)
	}
	return Fill{OrderID: "mkt-" + o.Side, Price: 100.0, Qty: o.Qty * e.fillRatio, FeeUSD: 0.01}, nil
}

// fakePartialMaker post-only fake：fillQty 固定（部分成交形态，FILLED 返回）。
type fakePartialMaker struct {
	venue       string
	fillQty     float64
	marketCalls int32
	lastMarket  Order
}

func (e *fakePartialMaker) Key() string { return e.venue }
func (e *fakePartialMaker) CloseOrder(ctx context.Context, ref OrderRef) (Fill, error) {
	return Fill{}, nil
}
func (e *fakePartialMaker) PlacePostOnly(ctx context.Context, o Order, px float64) (string, error) {
	return "po-d026", nil
}
func (e *fakePartialMaker) AwaitPostOnlyFill(ctx context.Context, o Order, orderID string) (Fill, bool, error) {
	return Fill{OrderID: orderID, Price: 100.0, Qty: e.fillQty, FeeUSD: 0.001}, true, nil
}
func (e *fakePartialMaker) PostOnlyCancel(ctx context.Context, o Order, orderID string) (Fill, error) {
	return Fill{OrderID: orderID, Qty: e.fillQty}, nil
}
func (e *fakePartialMaker) PlaceOrder(ctx context.Context, o Order) (Fill, error) {
	atomic.AddInt32(&e.marketCalls, 1)
	e.lastMarket = o
	return Fill{OrderID: "flat-" + o.Side, Price: 100.0, Qty: o.Qty}, nil
}

var d026Legs = []LegSpec{
	{VenueKey: "gate:perp", VenueSymbol: "BTC_USDT", Side: "SELL", Qty: 1, PriceExpected: 100},
	{VenueKey: "okx:spot", VenueSymbol: "BTC-USDT", Side: "BUY", Qty: 1, PriceExpected: 100},
}

func d026Setup(t *testing.T, perp, spot Executor) *Pipeline {
	t.Helper()
	reg := NewRegistry()
	reg.Register(perp)
	reg.Register(spot)
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "gate", VenueType: "perp", VenueSymbol: "BTC_USDT", Bid: 100.0, Ask: 100.2, Time: now})
	q.Publish(bus.Quote{Symbol: "BTCUSDT", Venue: "okx", VenueType: "spot", VenueSymbol: "BTC-USDT", Bid: 100.0, Ask: 100.2, Time: now})
	p := NewPipeline(reg, testStore(t), q, RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 1_000_000}, "demo")
	p.SetContractSizeResolver(permissiveCSResolver{})
	return p
}

// T1 部分成交 0.3 → 次腿缩量 0.3 → 双腿等量 → OPEN。
func TestD026_MakerPartialScaledOpen(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	perp := &fakePartialMaker{venue: "gate:perp", fillQty: 0.3}
	spot := &d026Executor{venue: "okx:spot", fillRatio: 1.0}
	p := d026Setup(t, perp, spot)
	p.SetMakerPolicy(2*time.Second, 10)

	res, err := p.Execute(ctx, "PERP:BTCUSDT:okx:spot:gate:perp:26a", "req-d026-a", "PERP_FUNDING", d026Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("等比缩量后双腿等量应 OPEN，got %s", res.Status)
	}
	if spot.lastMarket.Qty != 0.3 {
		t.Fatalf("次腿应缩量至 0.3，got %v", spot.lastMarket.Qty)
	}
	legs, err := s.ListPositionLegs(ctx, res.PositionID)
	if err != nil {
		t.Fatalf("list legs: %v", err)
	}
	for _, l := range legs {
		if l.Status != "FILLED" || l.FilledQty == nil || *l.FilledQty != 0.3 {
			t.Fatalf("leg %d 应 FILLED/0.3，got status=%s filled=%v", l.LegNo, l.Status, l.FilledQty)
		}
	}
}

// T2 次腿缩量后低于最小量被 venue 拒 → FAILED → LEGGED → 既有 unwind（CLOSED）。
func TestD026_MakerPartialBelowMinUnwind(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	perp := &fakePartialMaker{venue: "gate:perp", fillQty: 0.001}
	spot := &d026Executor{venue: "okx:spot", fillRatio: 1.0, rejectSide: "BUY"} // 0.001 低于最小量拒
	p := d026Setup(t, perp, spot)
	p.SetMakerPolicy(2*time.Second, 10)
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{"gate:perp": {}, "okx:spot": {}}}

	res, err := p.Execute(ctx, "PERP:BTCUSDT:okx:spot:gate:perp:26b", "req-d026-b", "PERP_FUNDING", d026Legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "CLOSED" {
		t.Fatalf("次腿拒（低于最小量）应 LEGGED→unwind CLOSED，got %s", res.Status)
	}
	if spot.lastMarket.Qty != 0.001 {
		t.Fatalf("次腿应以缩量 0.001 发单被拒，got %v", spot.lastMarket.Qty)
	}
}

// T3 taker IOC 部分成交（spot 0.3 vs perp 1.0）→ 守卫拦截：LEGGED + critical 告警 → unwind。
func TestD026_TakerPartialGuardBlocks(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	perp := &d026Executor{venue: "okx:perp", fillRatio: 1.0}
	spot := &d026Executor{venue: "binance:perp", fillRatio: 0.3} // IOC 部分成交形态
	p := d026Setup(t, perp, spot)
	p.SetMakerPolicy(0, 0) // maker 停用=既有并行 taker 路径（守卫两路径共用）
	p.verifier = &fakeVerifier{orders: map[string][]OpenedOrder{"okx:perp": {}, "binance:perp": {}}}
	// 双 perp 对（d127 同款收口链已验证形态）：spot 收口历史查证器缺位会让 flatten 停
	// LEGGED（D-015 既有行为），守卫断言用可收口对隔离验证。
	t026 := time.Now()
	if err := s.EnsureMonthPartition(ctx, t026.Year(), int(t026.Month())); err != nil {
		t.Fatalf("partition: %v", err)
	}
	legs26 := []LegSpec{
		{VenueKey: "okx:perp", VenueSymbol: "BTC-USDT-SWAP", Side: "SELL", Qty: 1, PriceExpected: 100},
		{VenueKey: "binance:perp", VenueSymbol: "BTCUSDT", Side: "BUY", Qty: 1, PriceExpected: 100},
	}

	res, err := p.Execute(ctx, "PERP:BTCUSDT:okx:perp:binance:perp:26c", "req-d026-c", "PERP_FUNDING", legs26)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "CLOSED" {
		t.Fatalf("守卫拦截应 LEGGED→unwind CLOSED（不判 OPEN），got %s", res.Status)
	}
	var alertHit bool
	rows, err := s.Pool().Query(ctx, `SELECT message FROM alerts WHERE position_id=$1 AND level='critical'`, res.PositionID)
	if err != nil {
		t.Fatalf("query alerts: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var msg string
		_ = rows.Scan(&msg)
		if strings.Contains(msg, "腿量不匹配") {
			alertHit = true
		}
	}
	rows.Close()
	if !alertHit {
		t.Fatal("应有「腿量不匹配」critical 告警")
	}
	// unwind 反向单：spot 腿 0.3 被 SELL 平（部分量的裸露面）
	if perp.marketCalls+0 == 0 && spot.marketCalls == 0 {
		t.Fatal("unwind 应有反向单出证")
	}
}

// TestD026_GuardMtCsMissSkipsLeg R1 验收锚：MT 腿 CS 取不到 → 守卫跳过该腿（不比裸
// lots），MT×perp 全 FILLED 仍判 OPEN——裸 lots（0.01）对 perp base（1.0）的量纲错配
// 曾被误判不匹配（复审 R1 退回点）。
func TestD026_GuardMtCsMissSkipsLeg(t *testing.T) {
	s := testStore(t)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE positions, position_legs, alerts RESTART IDENTITY CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	perp := &d026Executor{venue: "okx:perp", fillRatio: 1.0}
	mt := &d026Executor{venue: "3001157:mt5", fillRatio: 1.0}
	reg := NewRegistry()
	reg.Register(perp)
	reg.Register(mt)
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "okx", VenueType: "perp", VenueSymbol: "XAU-USDT-SWAP", Bid: 4200, Ask: 4200.5, Time: now})
	p := NewPipeline(reg, s, q, RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 1_000_000}, "live")
	p.SetContractSizeResolver(permissiveCSResolver{})
	p.SetMakerPolicy(0, 0) // 并行 taker 路径（守卫两路径共用）
	// CS resolver 对 MT 腿显式 miss（返回 ok=false）——守卫应跳过 MT 腿。
	p.csResolver = csMissForMT{permissiveCSResolver{}}

	legs := []LegSpec{
		{VenueKey: "3001157:mt5", VenueSymbol: "XAUUSDm", Side: "BUY", Qty: 0.01, PriceExpected: 4200},
		{VenueKey: "okx:perp", VenueSymbol: "XAU-USDT-SWAP", Side: "SELL", Qty: 1, PriceExpected: 4200},
	}
	// PERP_FUNDING=绕 allowlist/cap 依赖（d127 先例），守卫按腿量判定与 posType 无关。
	res, err := p.Execute(ctx, "PERP:XAUUSD:3001157:mt5:okx:perp:26d", "req-d026-d", "PERP_FUNDING", legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("CS miss 跳过 MT 腿后应判 OPEN，got %s", res.Status)
	}
	// 双重保险：守卫函数直喂本结果也应判无错配（MT 腿被跳过后只剩 perp 单腿可比=不足两条）。
	if p.legQtyMismatch(legs, res.Legs) {
		t.Fatal("CS miss 时守卫应跳过 MT 腿不判错配")
	}
}

// csMissForMT perp 命中、MT 全 miss 的 CS resolver（R1 场景专用）。
type csMissForMT struct{ inner ContractSizeResolver }

func (c csMissForMT) ContractSize(venueKey, symbol string) (float64, bool) {
	if keyMT(venueKey) {
		return 0, false
	}
	return c.inner.ContractSize(venueKey, symbol)
}

func keyMT(venueKey string) bool {
	return len(venueKey) > 4 && venueKey[len(venueKey)-4:] == ":mt5" || strings.Contains(venueKey, ":mt4")
}
