package autotrade

// D-330 XASSET 黄金 demo 对放行链测试锚 T5-T7。
// SSOT=docs/D-329+D-330设计与施工提示词-腿名义匹配与黄金demo放行-2026-09-18-Devin.md §3。
//
// T5 engine_opp fallback 锚：XAUUSD 买腿 perLot=$435,500 → qty 0.0023 < 0.01 minStep——
// 现码静默吞卡（断层②，先红=0 行日志）；修后 spec 驱动可行量下限产 intent（disabled 预拒
// 落 REJECTED 行捕获 qty=0.01）；对照 MaxGrossUSD=5000（8710>帽）仍静默跳过。
// T6 gate SkipNotional 锚：XASSET intent $4355 > MaxNotional $1000 放行（敞口帽=combo spec
// MaxGrossUSD pipeline 终审，同 SWAPARB 豁免先例 d108_p3）；MEANREVERT 同场景仍拒（回归）。
// T7 CLUSDT 回归锚：qty≥minStep 正常路径不消费 spec.MaxLongQty。

import (
	"context"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
)

// d330Setup DSN 库 + RUNNING 状态（策略不禁用；T5/T7 自行种 disabled 拦截 intent）。
func d330Setup(t *testing.T) *pgstore.Store {
	t.Helper()
	s := testAccumStore(t)
	d311Truncate(t, s)
	ctx := context.Background()
	if _, err := s.Pool().Exec(ctx, `TRUNCATE autotrade_state, strategy_stats CASCADE`); err != nil {
		t.Fatalf("truncate state: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert RUNNING: %v", err)
	}
	// D-014：MT 腿 carry 源——swap_free 账户（swap=0/account_flag）+佣金 0（venue 事实）。
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO broker_accounts (venue, venue_type, host, port, password, commission_bps, swap_free)
		VALUES ('277842155','mt5','h',443,'p',0,true) ON CONFLICT (venue) DO UPDATE SET swap_free=true, commission_bps=0`); err != nil {
		t.Fatalf("seed broker swap_free: %v", err)
	}
	if _, err := s.Pool().Exec(ctx, `
		INSERT INTO fin_xasset_monitor (pair, binance_bid, binance_ask, exness_bid, exness_ask,
			entry_spread_bps, exit_spread_bps, entry_net_bps, funding_rate_8h, funding_annual_pct,
			tracking_error_bps, equiv_annual_pct, noise_bps, harvest_floor_bps, cex_venue, mt_venue)
		VALUES ('XAUUSDT-XAU', 4355, 4356, 4354, 4355, 12, 5, 3, 0.001, 21.9, 1.0, 24.9, 9, 4.73,
			'binance:perp','277842155:mt5')`); err != nil {
		t.Fatalf("seed monitor: %v", err)
	}
	return s
}

// d330SeedDisabled 把 XASSET_CARRY 标记 disabled：gate 在 check2 早拒（intent 已产出的
// 确定性观测点）——rejectLog 落 autotrade_logs 含 qty，既证明 intent 产出又避免真实执行。
func d330SeedDisabled(t *testing.T, s *pgstore.Store) {
	t.Helper()
	if _, err := s.Pool().Exec(context.Background(),
		`INSERT INTO strategy_stats (strategy, disabled) VALUES ('XASSET_CARRY', true)`); err != nil {
		t.Fatalf("seed disabled: %v", err)
	}
}

// d330RegimeHealthy 种卖腿 funding 健康正样本（13 期 ≥floor，过 D-328 持久性闸）。
func d330RegimeHealthy(t *testing.T, s *pgstore.Store) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < 13; i++ {
		if _, err := s.Pool().Exec(ctx,
			// D-027：carry 判定改 7d 滚动均值——基底抬到 17（均值 23%/yr）保持入场闸放行。
			`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
			 VALUES ('funding', 'binance', 'XAUUSDT', $1, 'pct_annualized', $2, 'd330test')`,
			17.0+float64(i), time.Now().Add(-time.Duration(13-i)*5*time.Minute)); err != nil {
			t.Fatalf("seed funding fact %d: %v", i, err)
		}
	}
}

// d330GoldenOpp 断层②形态：XAUUSD@277842155 CS=100oz/lot → perLot≈$435.5k →
// $1000 预算 qty=0.0023 < 0.01 minStep。
func d330GoldenOpp() pgstore.Opportunity {
	return pgstore.Opportunity{
		ID: "XASSET_CARRY:XAUUSD:277842155:mt5:binance:perp", Type: "XASSET_CARRY",
		Canonical: "XAUUSD", CanonicalSell: d311strptr("XAUUSDT"),
		VenueBuy: "277842155:mt5", VenueSell: "binance:perp", NetBps: 18.09,
	}
}

// d330XAUService 构造 dashboard.Service：XAUUSD 腿 CS 与 ask 报价可解析（perLot=CS×ask）。
func d330XAUService(cs float64, ask float64) (*dashboard.Service, *bus.QuoteBus) {
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "277842155", VenueType: "mt5", Bid: ask - 0.5, Ask: ask, Time: time.Now(), VenueSymbol: "XAUUSDm"})
	// cex 腿报价显式 opt-in（d330CexFreshQuote / 各测试自播）——不预播防串场。
	svc := dashboard.New(nil, qb, nil, nil, nil, 0, 0, 0, 0, 0, 0, nil, nil, nil, 0, nil, nil, 0, 0, nil)
	svc.SetLegContractSize(func(venueKey, venueSymbol string) (float64, bool) {
		if venueKey == "277842155:mt5" {
			return cs, true
		}
		return 0, false
	})
	return svc, qb
}

// d330CexFreshQuote D-563 S4a：入场安全边际=双腿实时报价现算——cex 腿新鲜报价
// （≈21bps > monitor noise 9），达 sizing 需显式播种（夹具不预播防串场）。
func d330CexFreshQuote(qb *bus.QuoteBus, ask float64) {
	qb.Publish(bus.Quote{Symbol: "XAUUSDT", Venue: "binance", VenueType: "perp", Bid: ask * 1.002, Ask: ask*1.002 + 0.5, Time: time.Now()})
}

// d330Engine T5/T7 公共 engine：real gate（RUNNING 库）+ 白名单 spec + regime 配置。
func d330Engine(s *pgstore.Store, svc *dashboard.Service, qb *bus.QuoteBus, spec exec.XAssetComboAllowSpec) *Engine {
	return &Engine{
		store: s, service: svc, quotes: qb, gate: NewGate(s, Config{}, nil),
		csMissCounts: map[string]int{}, csMissSent: map[string]time.Time{},
		cfg: Config{
			MinNetBps:         0,
			NotionalUSD:       1000,
			AllowVenues:       map[string]bool{"277842155:mt5": true, "binance:perp": true},
			XAssetAllowCombos: []exec.XAssetComboAllowSpec{spec},
			CarryRegime:       exec.CarryRegimeConfig{Window: 24 * time.Hour, FloorPct: 2.0, MinSamples: 12},
			EconFeeBps:        d014FeeAny, // D-014 入场闸费率源（测试 fake：MT 0/CEX 5bps）
		},
	}
}

func d330CountLogs(t *testing.T, s *pgstore.Store, oppID string) int {
	t.Helper()
	var n int
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT count(*) FROM autotrade_logs WHERE opportunity_id = $1`, oppID).Scan(&n); err != nil {
		t.Fatalf("count logs: %v", err)
	}
	return n
}

func d330LastQty(t *testing.T, s *pgstore.Store, oppID string) float64 {
	t.Helper()
	var qty float64
	if err := s.Pool().QueryRow(context.Background(),
		`SELECT qty FROM autotrade_logs WHERE opportunity_id = $1 ORDER BY ts DESC LIMIT 1`, oppID).Scan(&qty); err != nil {
		t.Fatalf("query qty: %v", err)
	}
	return qty
}

// TestD330_XassetQtyFloorFromComboSpec T5 主锚：spec{MaxLongQty:0.01,MaxGrossUSD:10000}
// → fallback 产 intent qty=0.01（notional=qty×perLot≈$4355）。
// 红=现码静默 skip（0 行）；绿=REJECTED 行 qty=0.01。
func TestD330_XassetQtyFloorFromComboSpec(t *testing.T) {
	s := d330Setup(t)
	d330SeedDisabled(t, s)
	d006LiftCanonicalCap(t, s, "XAUUSD", "XAUUSDT")
	d330RegimeHealthy(t, s)
	ctx := context.Background()
	svc, qb := d330XAUService(100, 4355) // CS=100oz/lot → perLot=$435,500
	spec := exec.XAssetComboAllowSpec{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT",
		VenueBuy: "277842155:mt5", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUSD: 10000,
	}
	d330CexFreshQuote(qb, 4355)
	opp := d330GoldenOpp()
	e := d330Engine(s, svc, qb, spec)
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if n := d330CountLogs(t, s, opp.ID); n != 1 {
		t.Fatalf("fallback 应产 intent（disabled 预拒落 1 行）, got %d 行（红=现码静默 skip 0 行）", n)
	}
	if qty := d330LastQty(t, s, opp.ID); absFloat(qty-0.01) > 1e-9 {
		t.Errorf("intent qty=%v, want 0.01（spec.MaxLongQty 可行量下限）", qty)
	}
}

// TestD330_XassetQtyFloor_GrossCapFallback T5 对照：MaxGrossUSD=5000（双腿 gross 8710 超帽）
// → 维持静默跳过（0 行）。变异锚：摘 spec gross 条件 → 本例产 intent → 红。
func TestD330_XassetQtyFloor_GrossCapFallback(t *testing.T) {
	s := d330Setup(t)
	d330SeedDisabled(t, s)
	d330RegimeHealthy(t, s)
	ctx := context.Background()
	svc, qb := d330XAUService(100, 4355)
	spec := exec.XAssetComboAllowSpec{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT",
		VenueBuy: "277842155:mt5", VenueSell: "binance:perp",
		MaxLongQty: 0.01, MaxShortQty: 5, MaxGrossUSD: 5000, // 2×0.01×435500=8710 > 5000
	}
	d330CexFreshQuote(qb, 4355)
	opp := d330GoldenOpp()
	e := d330Engine(s, svc, qb, spec)
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if n := d330CountLogs(t, s, opp.ID); n != 0 {
		t.Fatalf("gross 超帽应维持静默跳过, got %d 行", n)
	}
}

// TestD330_GateSkipNotionalXAsset T6：XASSET intent $4355 > MaxNotional $1000 → 放行
// （SkipNotional 扩型；敞口帽=XAssetComboAllowSpec.MaxGrossUSD pipeline 事后校验，同 SWAPARB
// 豁免 d108_p3 先例）；MEANREVERT 同场景仍拒（回归）；gateDecision 纯函数层 XASSET 豁免语义锚。
func TestD330_GateSkipNotionalXAsset(t *testing.T) {
	s := d330Setup(t)
	ctx := context.Background()
	g := NewGate(s, Config{MaxDailyLoss: 1000, MaxPositions: 5, MaxNotional: 1000, LiveTrade: true}, nil)
	mkIntent := func(strategy string) *TradeIntent {
		return &TradeIntent{
			Strategy: strategy, Canonical: "XAUUSD",
			VenueBuy: "277842155:mt5", VenueSell: "binance:perp",
			Qty: 0.01, NotionalUSD: 4355, DedupKey: "D330GATE:" + strategy, NetBps: 18,
			Execute: func(context.Context) (string, string, int64) { return "EXECUTED", "", 0 },
		}
	}
	if r := g.Submit(ctx, mkIntent("XASSET_CARRY")); r.Status != "EXECUTED" {
		t.Errorf("XASSET notional $4355>$1000 应 SkipNotional 放行, got %s (%s)（红=现码 notional 拒）", r.Status, r.Reason)
	}
	if r := g.Submit(ctx, mkIntent("MEANREVERT")); r.Status != "REJECTED" {
		t.Errorf("MEANREVERT 回归: notional $4355>$1000 应拒, got %s (%s)", r.Status, r.Reason)
	}
	// gateDecision 纯函数层：SkipNotional=true 豁免名义帽（同 SWAPARB/CARRY 语义，D-187 先例）
	v := gateDecision(GateInput{
		State: StateRunning, Strategy: "XASSET_CARRY",
		MaxDailyLoss: 1000, MaxPositions: 5,
		NotionalUSD: 4355, MaxNotional: 1000, SkipNotional: true, DedupHit: false,
	})
	if !v.Pass {
		t.Errorf("gateDecision XASSET SkipNotional=true 应放行, got reject: %s", v.Reason)
	}
}

// TestD330_NormalPathIgnoresSpecMaxLongQty T7：qty≥minStep 正常路径不消费 spec.MaxLongQty
// （CLUSDT 回归锚）——CS=1/ask=96.6 → qty=10.35（非 9999），pass-1 平衡路径行为零变化。
func TestD330_NormalPathIgnoresSpecMaxLongQty(t *testing.T) {
	s := d330Setup(t)
	d330SeedDisabled(t, s)
	d006LiftCanonicalCap(t, s, "XAUUSD", "XAUUSDT")
	d330RegimeHealthy(t, s)
	ctx := context.Background()
	// D-014：小名义（$1000）下 17%/yr 回本≈7.7d>3d 会被 econ 入场闸拦——
	// 补一批 150%/yr funding fact 使回本≤3d（本测试目标是 qty 正常路径非回本闸）。
	// D-027：判定改 7d 滚动均值——需一组样本拉高均值而非单条瞬时值。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO facts (kind, venue, symbol, value, unit, ts, src)
		 SELECT 'funding','binance','XAUUSDT',150.0,'pct_annualized',
			now() - (i || ' minutes')::interval,'d330test' FROM generate_series(0,47) i`); err != nil {
		t.Fatalf("seed fresh funding: %v", err)
	}
	svc, qb := d330XAUService(1, 96.6) // perLot=$96.6 → qty=1000/96.6≈10.352 ≥ 0.01
	spec := exec.XAssetComboAllowSpec{
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSDT",
		VenueBuy: "277842155:mt5", VenueSell: "binance:perp",
		MaxLongQty: 9999, MaxShortQty: 5, MaxGrossUSD: 100000,
	}
	d330CexFreshQuote(qb, 96.6)
	opp := d330GoldenOpp()
	e := d330Engine(s, svc, qb, spec)
	e.handleOpportunity(ctx, opp, newComboAccumulator())
	if n := d330CountLogs(t, s, opp.ID); n != 1 {
		t.Fatalf("正常路径应产 intent, got %d 行", n)
	}
	qty := d330LastQty(t, s, opp.ID)
	if absFloat(qty-1000/96.6) > 1e-4 {
		t.Errorf("intent qty=%v, want %v（notional/perLot，不消费 MaxLongQty=9999）", qty, 1000/96.6)
	}
	if qty > 100 {
		t.Errorf("intent qty=%v 被 spec.MaxLongQty 污染（回归破锚）", qty)
	}
}
