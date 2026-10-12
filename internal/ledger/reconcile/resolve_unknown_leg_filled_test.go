// d421_resolve_test.go —— D-421：UNKNOWN 消解器 + LEGGED 收编前置守卫。
package reconcile

import (
	"context"
	"errors"
	"testing"

	"nox/internal/store/pgstore"
)

// —— fake OrderStatusQuerier ——

type d421OSQ struct {
	state string
	qty   float64
	px    float64
	fee   float64
	err   error
	calls int
}

func (q *d421OSQ) QueryOrderStatus(_ context.Context, _, _, _ string) (string, float64, float64, float64, error) {
	q.calls++
	return q.state, q.qty, q.px, q.fee, q.err
}

// —— fakeStore 扩展（D-421 接口新增；实现同文件供断言）——

func (f *fakeStore) ListUnknownLegs(ctx context.Context, limit int) ([]pgstore.PositionLeg, error) {
	var out []pgstore.PositionLeg
	for _, ls := range f.legs {
		for _, l := range ls {
			if l.Status == "UNKNOWN" && l.OrderID != "" {
				out = append(out, *l)
			}
		}
	}
	return out, nil
}

func (f *fakeStore) ResolveLegFill(ctx context.Context, legID int64, price, qty, feeUSD float64) error {
	for _, ls := range f.legs {
		for _, l := range ls {
			if l.ID == legID {
				l.Status = "FILLED"
				l.PriceActual = &price
				l.FilledQty = &qty
				l.AvgFillPrice = &price
				l.FeeUSD = &feeUSD
				return nil
			}
		}
	}
	return errors.New("leg not found")
}

func (f *fakeStore) ResolveLegFail(ctx context.Context, legID int64) error {
	for _, ls := range f.legs {
		for _, l := range ls {
			if l.ID == legID {
				l.Status = "FAILED"
				return nil
			}
		}
	}
	return errors.New("leg not found")
}

func (f *fakeStore) SetPositionUncertainty(ctx context.Context, id int64, reason string) error {
	if p, ok := f.positions[id]; ok {
		p.UncertaintyReason = &reason
	}
	return nil
}

func d421SeedUnknown() (*fakeStore, *pgstore.Position, *pgstore.PositionLeg) {
	fs := newFakeStore()
	reason := "okx fill unconfirmed"
	p := &pgstore.Position{ID: 1, Status: "CLOSING", UncertaintyReason: &reason}
	fs.positions[1] = p
	leg := &pgstore.PositionLeg{
		ID: 11, PositionID: 1, LegNo: 0, Phase: "OPEN", Venue: "okx:perp",
		Symbol: "ETH-USDT-SWAP", Side: "buy", Qty: 0.1,
		OrderID: "O421", ClientOrderID: "c421", Status: "UNKNOWN",
	}
	filled := 0.1
	leg2 := &pgstore.PositionLeg{
		ID: 12, PositionID: 1, LegNo: 1, Phase: "OPEN", Venue: "3001157:mt5",
		Symbol: "XAUUSDm", Side: "sell", Qty: 0.01, OrderID: "t9", Status: "FILLED",
		FilledQty: &filled,
	}
	fs.legs[1] = []*pgstore.PositionLeg{leg, leg2}
	return fs, p, leg
}

// T5a 消解器：CEX UNKNOWN 腿查单 filled → FILLED 回填 + 全腿齐 → 仓 OPEN + 清 uncertainty。
func TestD421ResolveUnknownLegFilled(t *testing.T) {
	fs, _, leg := d421SeedUnknown()
	r := New(fs)
	r.SetOrderStatusQueriers(map[string]OrderStatusQuerier{
		"okx:perp": &d421OSQ{state: "filled", qty: 0.1, px: 3000, fee: 0.05},
	})
	r.resolveUnknownLegs(context.Background())

	if leg.Status != "FILLED" || *leg.PriceActual != 3000 || *leg.FilledQty != 0.1 {
		t.Fatalf("腿应回填 FILLED price=3000 qty=0.1，got %+v", leg)
	}
	p := fs.positions[1]
	if p.Status != "OPEN" {
		t.Fatalf("全腿 FILLED 应仓 OPEN，got %s", p.Status)
	}
	if p.UncertaintyReason != nil && *p.UncertaintyReason != "" {
		t.Fatalf("uncertainty 应清除，got %v", *p.UncertaintyReason)
	}
}

// T5b canceled+零成交 → leg FAILED → 仓 LEGGED（进 D-414 收编域）+ 清 uncertainty。
func TestD421ResolveUnknownLegCanceled(t *testing.T) {
	fs, _, leg := d421SeedUnknown()
	r := New(fs)
	r.SetOrderStatusQueriers(map[string]OrderStatusQuerier{
		"okx:perp": &d421OSQ{state: "canceled", qty: 0},
	})
	r.resolveUnknownLegs(context.Background())

	if leg.Status != "FAILED" {
		t.Fatalf("canceled+零成交应 FAILED，got %s", leg.Status)
	}
	if fs.positions[1].Status != "LEGGED" {
		t.Fatalf("应转 LEGGED 进 D-414 收编域，got %s", fs.positions[1].Status)
	}
}

// T5c 持续不可判 ≥10 轮 → critical + 留 UNKNOWN 人工。
func TestD421ResolveUnknownLegStuckCritical(t *testing.T) {
	fs, _, leg := d421SeedUnknown()
	r := New(fs)
	r.SetOrderStatusQueriers(map[string]OrderStatusQuerier{
		"okx:perp": &d421OSQ{state: "live"},
	})
	for range 10 {
		r.resolveUnknownLegs(context.Background())
	}
	if leg.Status != "UNKNOWN" {
		t.Fatalf("不可判应留 UNKNOWN，got %s", leg.Status)
	}
	var crit bool
	for _, a := range fs.alerts {
		if a.level == "critical" {
			crit = true
		}
	}
	if !crit {
		t.Fatal("≥10 轮不可判应发 critical")
	}
}

// T5d MT UNKNOWN 腿经 OrderHistoryQuerier 消解（D-416 在册面复用）。
func TestD421ResolveUnknownMTLeg(t *testing.T) {
	fs := newFakeStore()
	reason := "MT timeout"
	p := &pgstore.Position{ID: 2, Status: "CLOSING", UncertaintyReason: &reason}
	fs.positions[2] = p
	filled := 0.01
	mtLeg := &pgstore.PositionLeg{
		ID: 21, PositionID: 2, LegNo: 0, Phase: "OPEN", Venue: "3001157:mt5",
		Symbol: "XAUUSDm", Side: "buy", Qty: 0.01, OrderID: "2553556210", Status: "UNKNOWN",
	}
	other := &pgstore.PositionLeg{
		ID: 22, PositionID: 2, LegNo: 1, Phase: "OPEN", Venue: "okx:perp",
		Status: "FILLED", FilledQty: &filled,
	}
	fs.legs[2] = []*pgstore.PositionLeg{mtLeg, other}

	r := New(fs)
	r.SetOrderHistoryQueriers(&fakeHistQ{venue: "3001157:mt5",
		hit: &HistoryOrder{Ticket: "2553556210", ClosePrice: 4316.242, Profit: 2.61}})
	r.resolveUnknownLegs(context.Background())

	if mtLeg.Status != "FILLED" || *mtLeg.PriceActual != 4316.242 {
		t.Fatalf("MT 腿应按历史查证回填 FILLED，got %+v", mtLeg)
	}
	if fs.positions[2].Status != "OPEN" {
		t.Fatalf("got %s", fs.positions[2].Status)
	}
}

type fakeHistQ struct {
	venue string
	hit   *HistoryOrder
	err   error
}

func (q *fakeHistQ) VenueKey() string { return q.venue }
func (q *fakeHistQ) QueryOrderHistory(_ context.Context, _ string, _ int) (*HistoryOrder, error) {
	return q.hit, q.err
}

// —— T6 LEGGED 收编前置守卫 ——

func d421SeedLegged(status2 string) (*fakeStore, *fakeCloser) {
	fs := newFakeStore()
	p := &pgstore.Position{ID: 3, Status: "LEGGED"}
	fs.positions[3] = p
	fq := 0.1
	fs.legs[3] = []*pgstore.PositionLeg{
		{ID: 31, PositionID: 3, LegNo: 0, Phase: "OPEN", Venue: "3001157:mt5",
			Status: "FILLED", FilledQty: &fq, OrderID: "t1"},
		{ID: 32, PositionID: 3, LegNo: 1, Phase: "OPEN", Venue: "okx:perp",
			Status: status2, OrderID: "O421"},
	}
	c := &fakeCloser{store: fs}
	return fs, c
}

// T6a FAILED 腿查单 accFillSz>0 → 回填 FILLED + 中止收编 + 仓 OPEN。
func TestD421LeggedPreflightFilledAborts(t *testing.T) {
	fs, c := d421SeedLegged("FAILED")
	r := New(fs)
	r.SetCloser(c)
	r.SetOrderStatusQueriers(map[string]OrderStatusQuerier{
		"okx:perp": &d421OSQ{state: "filled", qty: 0.1, px: 3000},
	})
	r.fastRetryLegged(context.Background(), fs.positions[3])

	if len(c.calls) != 0 {
		t.Fatalf("FAILED 腿实际已成交 → 必须中止收编，got closer calls=%v", c.calls)
	}
	if fs.legs[3][1].Status != "FILLED" {
		t.Fatalf("腿应回填 FILLED，got %s", fs.legs[3][1].Status)
	}
	if fs.positions[3].Status != "OPEN" {
		t.Fatalf("全腿齐应仓 OPEN（真实持仓非残腿），got %s", fs.positions[3].Status)
	}
}

// R1 锚定：FAILED 腿查单 canceled+qty>0（撤单前已部分成交）→ 走 filled 分支：
// ResolveLegFill 回填 + reevalAfterResolve + return false 中止收编。
func TestD421LeggedPreflightCanceledPartialAborts(t *testing.T) {
	fs, c := d421SeedLegged("FAILED")
	r := New(fs)
	r.SetCloser(c)
	r.SetOrderStatusQueriers(map[string]OrderStatusQuerier{
		"okx:perp": &d421OSQ{state: "canceled", qty: 0.05, px: 2990},
	})
	r.fastRetryLegged(context.Background(), fs.positions[3])

	if len(c.calls) != 0 {
		t.Fatalf("canceled+部分成交=真实持仓 → 必须中止收编，got closer calls=%v", c.calls)
	}
	l := fs.legs[3][1]
	if l.Status != "FILLED" || *l.FilledQty != 0.05 {
		t.Fatalf("腿应回填 FILLED qty=0.05，got %+v", l)
	}
	if fs.positions[3].Status != "OPEN" {
		t.Fatalf("全腿齐应仓 OPEN，got %s", fs.positions[3].Status)
	}
}

// T6b 查单零成交 → 继续既有收编（ClaimClose→ResumeClose）。
func TestD421LeggedPreflightEmptyContinues(t *testing.T) {
	fs, c := d421SeedLegged("FAILED")
	r := New(fs)
	r.SetCloser(c)
	r.SetOrderStatusQueriers(map[string]OrderStatusQuerier{
		"okx:perp": &d421OSQ{state: "canceled", qty: 0},
	})
	r.fastRetryLegged(context.Background(), fs.positions[3])
	if len(c.calls) == 0 {
		t.Fatal("确定未成交应继续收编（ResumeClose 被调）")
	}
}

// T6c 查询失败 → 仓→CLOSING+uncertainty+critical，本轮不平（fail-closed）。
func TestD421LeggedPreflightErrFailsClosed(t *testing.T) {
	fs, c := d421SeedLegged("FAILED")
	r := New(fs)
	r.SetCloser(c)
	r.SetOrderStatusQueriers(map[string]OrderStatusQuerier{
		"okx:perp": &d421OSQ{err: errors.New("net err")},
	})
	r.fastRetryLegged(context.Background(), fs.positions[3])

	if len(c.calls) != 0 {
		t.Fatalf("查询失败必须本轮不平，got calls=%v", c.calls)
	}
	p := fs.positions[3]
	if p.UncertaintyReason == nil || *p.UncertaintyReason == "" {
		t.Fatal("查询失败应标 uncertainty 留人工")
	}
	var crit bool
	for _, a := range fs.alerts {
		if a.level == "critical" {
			crit = true
		}
	}
	if !crit {
		t.Fatal("查询失败应发 critical")
	}
}
