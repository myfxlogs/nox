// d032_realpath_test.go —— D-032 R1 锚：②事故谓词细化（FAILED∧∃FILLED 腿才计）+
// ③真实写路径（pipeline.Execute → notional_usd → VenueNotionalExposure → 权益闸 35% 阈按 $3000 计）。
package autotrade

import (
	"context"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// ② R1：事故谓词细化——零成交 FAILED 是干净终态不拦（#503/#504 形态）；
// FAILED ∧ ∃FILLED 腿（真裸露过）拦。
func TestD032_IncidentGate_FailedByFilledLeg(t *testing.T) {
	s := setupLiveGateStore(t, "vA:mt5", "vB:mt5", false, false)
	d032Truncate(t, s)
	ctx := context.Background()
	var posID int64
	if err := s.Pool().QueryRow(ctx,
		`INSERT INTO positions (opportunity_id, type, status, opened_at, closed_at)
		 VALUES ('D032:zfill','XASSET_CARRY','FAILED', now()-interval '1 hour', now()) RETURNING id`).Scan(&posID); err != nil {
		t.Fatalf("seed zero-fill failed: %v", err)
	}
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty, price_expected, status)
		 VALUES ($1,0,'OPEN',1,'okx','perp','XAU-USDT-SWAP','SELL',1,2500,'FAILED'),
		        ($1,1,'OPEN',1,'3001157','mt5','XAUUSDm','BUY',0.01,2500,'FAILED')`, posID); err != nil {
		t.Fatalf("seed failed legs: %v", err)
	}
	g := NewGate(s, Config{MaxDailyLoss: 100, MaxPositions: 5, RiskIncidentAckID: 0}, nil)
	intent := func() *TradeIntent {
		return &TradeIntent{
			Strategy: "XASSET_CARRY", Canonical: "XAUUSD",
			VenueBuy: "vA:mt5", VenueSell: "vB:mt5", Qty: 0.01, DedupKey: "D032:opp:z:vA:vB",
			Execute: func(context.Context) (string, string, int64) { return "EXECUTED", "", 1 },
		}
	}
	res := g.Submit(ctx, intent())
	if strings.Contains(res.Reason, "incident position") {
		t.Fatalf("锚R1a: 零成交 FAILED 不应拦开（干净终态），got %s (%s)", res.Status, res.Reason)
	}
	// 同仓补一条 FILLED 腿 → FAILED ∧ ∃FILLED → 拦。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO position_legs (position_id, leg_no, phase, attempt_no, venue, venue_type, symbol, side, qty, filled_qty, price_expected, price_actual, status)
		 VALUES ($1,2,'OPEN',1,'okx','perp','XAU-USDT-SWAP','SELL',1,1,2500,2500,'FILLED')`, posID); err != nil {
		t.Fatalf("seed filled leg: %v", err)
	}
	res = g.Submit(ctx, intent())
	if res.Status != "REJECTED" || !strings.Contains(res.Reason, "incident position") {
		t.Fatalf("锚R1b: 单腿成交 FAILED 应拦开（裸露过），got %s (%s)", res.Status, res.Reason)
	}
}

// ③ R1：真实写路径桩件——pipeline.Execute 落 notional_usd 的最小 fake。
type d032Executor struct {
	venue string
	px    float64
}

func (e *d032Executor) Key() string { return e.venue }
func (e *d032Executor) CloseOrder(context.Context, exec.OrderRef) (exec.Fill, error) {
	return exec.Fill{}, nil
}

// TradeModeNow MT 腿可平性探针（恒在市）。
func (e *d032Executor) TradeModeNow(context.Context, string) (int64, error) { return 1, nil }
func (e *d032Executor) PlaceOrder(_ context.Context, o exec.Order) (exec.Fill, error) {
	return exec.Fill{OrderID: "d032-" + e.venue, Price: e.px, Qty: o.Qty}, nil
}

type d032CS map[string]float64

func (d d032CS) ContractSize(vk, vs string) (float64, bool) { c, ok := d[vk+":"+vs]; return c, ok }

// d032SeedTicks LegActionable 可平性闸所需行情行（同 exec.seedActionableTicks 键域）。
func d032SeedTicks(t *testing.T, s *pgstore.Store, pairs ...[2]string) {
	t.Helper()
	ctx := context.Background()
	if err := s.EnsureMonthPartition(ctx, time.Now().Year(), int(time.Now().Month())); err != nil {
		t.Fatalf("ensure ticks partition: %v", err)
	}
	now := time.Now()
	var rows []pgstore.TickRow
	for _, pr := range pairs {
		sym := pr[1]
		if bus.IsCexVenueKey(pr[0]) {
			sym = bus.CexSymbolKey(sym)
		}
		rows = append(rows,
			pgstore.TickRow{Ts: now.Add(-2 * time.Minute), Venue: keydomain.BareOf(pr[0]), VenueType: keydomain.TypeOf(pr[0]), Symbol: sym, Bid: 99.9, Ask: 100.1},
			pgstore.TickRow{Ts: now, Venue: keydomain.BareOf(pr[0]), VenueType: keydomain.TypeOf(pr[0]), Symbol: sym, Bid: 100.0, Ask: 100.2})
	}
	if err := s.InsertTicks(ctx, rows); err != nil {
		t.Fatalf("seed ticks: %v", err)
	}
}

// ③ R1 锚：走真实 pipeline.Execute 写路径——XASSET 开仓落 notional_usd=Σ腿名义；
// 该仓贡献 venue 在仓敞口 $2000，新腿 $1000 → 35% 阈按 $3000 计（$1049 拒 / $1100 放）。
func TestD032_EquityGate_RealPathNotional(t *testing.T) {
	s := setupLiveGateStore(t, "3001157:mt5", "okx:perp", false, false)
	d032Truncate(t, s)
	ctx := context.Background()
	cs := "XAUUSD"
	oppID := "XASSET_CARRY:XAUUSD:3001157:mt5:okx:perp"
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "XAUUSD", CanonicalSell: &cs,
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp",
		QuoteTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opportunity: %v", err)
	}
	d032SeedTicks(t, s, [2]string{"3001157:mt5", "XAUUSDm"}, [2]string{"okx:perp", "XAU-USDT-SWAP"})
	reg := exec.NewRegistry()
	reg.Register(&d032Executor{venue: "3001157:mt5", px: 100})
	reg.Register(&d032Executor{venue: "okx:perp", px: 100})
	q := bus.New()
	now := time.Now()
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "3001157", VenueType: "mt5", VenueSymbol: "XAUUSDm", Bid: 99.9, Ask: 100.1, Time: now})
	q.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "okx", VenueType: "perp", VenueSymbol: "XAU-USDT-SWAP", Bid: 99.9, Ask: 100.1, Time: now})
	p := exec.NewPipeline(reg, s, q, exec.RiskLimits{MaxOpenPositions: 10, MaxNotionalUSD: 1e6}, "demo")
	p.SetContractSizeResolver(d032CS{"3001157:mt5:XAUUSDm": 50, "okx:perp:XAU-USDT-SWAP": 1})
	p.SetXAssetComboAllow([]exec.XAssetComboAllowSpec{{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSD",
		VenueBuy: "3001157:mt5", VenueSell: "okx:perp",
		MaxLongQty: 10, MaxShortQty: 100, MaxGrossUSD: 5000, // <DefaultCanonicalCapUSD 6000
	}})
	p.SetMarginInputs(nil, nil) // frac 未接线 → 腿按名义全款计（保守）
	// 双腿名义：mt 0.4 lots×CS50×$100=$2000；okx 20 张×CS1×$100=$2000（canonical 20=20 等量 → OPEN）。
	legs := []exec.LegSpec{
		{VenueKey: "3001157:mt5", VenueSymbol: "XAUUSDm", Side: "BUY", Qty: 0.4, PriceExpected: 100},
		{VenueKey: "okx:perp", VenueSymbol: "XAU-USDT-SWAP", Side: "SELL", Qty: 20, PriceExpected: 100},
	}
	res, err := p.Execute(ctx, oppID, "req-d032r1", "XASSET_CARRY", legs)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if res.Status != "OPEN" {
		t.Fatalf("锚R1c: want OPEN, got %s", res.Status)
	}
	// GetPosition SELECT 不含 notional_usd——直查列。
	var nusd *float64
	if err := s.Pool().QueryRow(ctx,
		`SELECT notional_usd FROM positions WHERE id=$1`, res.PositionID).Scan(&nusd); err != nil {
		t.Fatalf("read notional_usd: %v", err)
	}
	if nusd == nil || *nusd != 4000 {
		t.Fatalf("锚R1c: notional_usd 应为 Σ腿 $4000, got %v", nusd)
	}
	expo, err := s.VenueNotionalExposure(ctx, []string{"XASSET_CARRY", "FUNDING_HEDGE"})
	if err != nil {
		t.Fatalf("exposure: %v", err)
	}
	if expo["okx:perp"] != 2000 || expo["3001157:mt5"] != 2000 {
		t.Fatalf("锚R1c: venue 敞口应各 $2000, got %v", expo)
	}
	// 闸：okx 需 35%×(2000+1000)=$1050；mt 需 25%×3000=$750（mt 给 1e5 恒过）。
	cfg := Config{RiskCexEquityPct: 35, RiskCfdEquityPct: 25}
	e := &Engine{cfg: cfg, store: s, service: &d032Svc{eq: map[string]float64{"okx:perp": 1049, "3001157:mt5": 1e5}}}
	if !e.equityDisciplineGate(ctx, d032Opp(), 1000) {
		t.Fatal("锚R1c: okx $1049 < $1050 阈应拒开")
	}
	e = &Engine{cfg: cfg, store: s, service: &d032Svc{eq: map[string]float64{"okx:perp": 1100, "3001157:mt5": 1e5}}}
	if e.equityDisciplineGate(ctx, d032Opp(), 1000) {
		t.Fatal("锚R1c: okx $1100 ≥ $1050 阈应放行")
	}
}
