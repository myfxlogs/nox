package dashboard

// D-329 腿名义粗腿锚定匹配（两段式）测试锚 T1-T4。
// SSOT=docs/D-329+D-330设计与施工提示词-腿名义匹配与黄金demo放行-2026-09-18-Devin.md §3。
//
// T1 resolveLegs CLUSDT 锚：粗腿（USOILm 0.01lot≈$966）锚定，细腿（gate CL_USDT）回算
// 1034→1000 张——现码恒 abort（结构性失衡 1542 连拒），先红。
// T2 平衡路径不变锚：pass-2 不触发，LegSpec 与现码逐值相等。
// T3 双粗腿不可执行锚：新语义错误串 leg granularity unexecutable（与原 imbalance 文案区分）。
// T4 MeanRevertWithCap 同 helper 锚：同一失衡经 ExecuteMeanRevertWithCap 走 matchLegsNotional。

import (
	"context"
	"strings"
	"testing"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
	"nox/internal/trading/exec"
)

// d329LegsService 构造纯单元 Service：qbus + 双 CS/步长解析器（无 store——resolveLegs 不触库）。
func d329LegsService(t *testing.T, qb *bus.QuoteBus, cs map[string]float64, steps map[string]float64) *Service {
	t.Helper()
	// D-466：binance step 只认运行时表（fallback 已撤）——fixture 注入等价 rt 值。
	sm := NewCexStepMeta("http://unused", 0)
	sm.rt = map[string]float64{"BTCUSDT": 0.001}
	return &Service{
		stepMeta: sm,
		qbus:     qb,
		cexContractSize: func(venue, venueSymbol string) (float64, bool) {
			v, ok := cs[venue+":"+venueSymbol]
			return v, ok
		},
		legContractSize: func(venueKey, venueSymbol string) (float64, bool) {
			v, ok := cs[venueKey+":"+venueSymbol]
			return v, ok
		},
		legVolumeStep: func(venueKey, venueSymbol string) (float64, bool) {
			v, ok := steps[venueKey+":"+venueSymbol]
			return v, ok
		},
	}
}

// TestD329_ResolveLegs_CoarseAnchorMatch T1：CLUSDT 回放——买 gate CL_USDT（cs=0.01/step=1.0
// ≈$0.97/张）卖 USOILm（cs=1000/step=0.01 ≈$966/步），ref=96.6、qty=1034（买名义≈$999）。
// 现码：sellQty 取整 0.01→$966，恒 3.3% 失衡 abort（红）；修后：粗腿（卖）锚定，
// 细腿回算 1000 张 → 双腿 $966 imb=0 放行。
func TestD329_ResolveLegs_CoarseAnchorMatch(t *testing.T) {
	now := time.Now()
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "gate", VenueType: "perp", Bid: 96.5, Ask: 96.6, Time: now, VenueSymbol: "CL_USDT"})
	qb.Publish(bus.Quote{Symbol: "CLUSDT", Venue: "xxx", VenueType: "mt5", Bid: 96.6, Ask: 96.7, Time: now, VenueSymbol: "USOILm"})
	s := d329LegsService(t, qb, map[string]float64{
		"gate:CL_USDT": 0.01, "xxx:mt5:USOILm": 1000,
	}, map[string]float64{
		"xxx:mt5:USOILm": 0.01,
	})
	found := &pgstore.Opportunity{ID: "XASSET_CARRY:CLUSDT:gate:perp:xxx:mt5", Canonical: "CLUSDT", VenueBuy: "gate:perp", VenueSell: "xxx:mt5"}
	legs, err := s.resolveLegs(found, 1034)
	if err != nil {
		t.Fatalf("粗腿锚定应放行（红=现码 abort: %v）", err)
	}
	if len(legs) != 2 {
		t.Fatalf("legs len=%d, want 2", len(legs))
	}
	buy, sell := legs[0], legs[1]
	// 锚=sell（unit $966 > buy unit $0.966）：sellQty 不动 0.01，buyQty 回算 1000 张
	if buy.Qty != 1000 {
		t.Errorf("buy qty=%v, want 1000（1034→1000 回算）", buy.Qty)
	}
	if sell.Qty != 0.01 {
		t.Errorf("sell qty=%v, want 0.01（粗腿锚不动）", sell.Qty)
	}
	notionalBuy := buy.Qty * 0.01 * 96.6
	notionalSell := sell.Qty * 1000 * 96.6
	if imb := abs(notionalSell-notionalBuy) / notionalBuy; imb > 1e-9 {
		t.Errorf("imb=%.6f%%, want ≈0（匹配后平衡）", imb*100)
	}
	if buy.VenueSymbol != "CL_USDT" || buy.PriceExpected != 96.6 {
		t.Errorf("buy spec=%+v, want CL_USDT@96.6", buy)
	}
	if sell.VenueSymbol != "USOILm" || sell.PriceExpected != 96.6 {
		t.Errorf("sell spec=%+v, want USOILm@96.6(bid)", sell)
	}
}

func abs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

// TestD329_ResolveLegs_BalancedUnchanged T2：平衡路径不变锚——qty≥步长带内、pass-1 平衡，
// pass-2 不触发，LegSpec 数量/价格与现码逐值相等（XAUUSD MT×binance 形态）。
func TestD329_ResolveLegs_BalancedUnchanged(t *testing.T) {
	now := time.Now()
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "ic", VenueType: "mt5", Bid: 4354.5, Ask: 4355, Time: now, VenueSymbol: "XAUUSD"})
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "binance", VenueType: "perp", Bid: 4355, Ask: 4355.2, Time: now, VenueSymbol: "BTCUSDT"})
	s := d329LegsService(t, qb, map[string]float64{
		"ic:mt5:XAUUSD": 100, "binance:BTCUSDT": 1.0,
	}, map[string]float64{
		"ic:mt5:XAUUSD": 0.01,
	})
	found := &pgstore.Opportunity{ID: "SWAPARB:XAUUSD:ic:mt5:binance:perp", Canonical: "XAUUSD", VenueBuy: "ic:mt5", VenueSell: "binance:perp"}
	legs, err := s.resolveLegs(found, 0.01)
	if err != nil {
		t.Fatalf("平衡路径应原样放行: %v", err)
	}
	// 现码逐值：sellQty=round(0.01×100/1×100)/100=1.0；buy floor step0.01→0.01；sell floor step0.001→1.0
	if legs[0].Qty != 0.01 || legs[0].PriceExpected != 4355 {
		t.Errorf("buy=%+v, want qty 0.01 @4355（现码逐值）", legs[0])
	}
	if legs[1].Qty != 1.0 || legs[1].PriceExpected != 4355 {
		t.Errorf("sell=%+v, want qty 1.0 @4355（现码逐值）", legs[1])
	}
}

// TestD329_ResolveLegs_DualCoarseUnexecutable T3：双粗腿锚（两腿步长名义均超容差带，
// 买 $5000/卖 $4800、qty 带 $5000）→ 新语义错误串 leg granularity unexecutable（红：现码为旧 imbalance 文案）。
func TestD329_ResolveLegs_DualCoarseUnexecutable(t *testing.T) {
	now := time.Now()
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vA", VenueType: "mt5", Bid: 999, Ask: 1000, Time: now, VenueSymbol: "A1"})
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vB", VenueType: "mt5", Bid: 1000, Ask: 1000.2, Time: now, VenueSymbol: "B1"})
	s := d329LegsService(t, qb, map[string]float64{
		"vA:mt5:A1": 5, "vB:mt5:B1": 4.8,
	}, map[string]float64{
		"vA:mt5:A1": 1.0, "vB:mt5:B1": 1.0,
	})
	found := &pgstore.Opportunity{ID: "SWAPARB:XAUUSD:vA:mt5:vB:mt5", Canonical: "XAUUSD", VenueBuy: "vA:mt5", VenueSell: "vB:mt5"}
	_, err := s.resolveLegs(found, 1)
	if err == nil {
		t.Fatal("双粗腿结构性不可开仓应报错")
	}
	if !strings.Contains(err.Error(), "腿粒度无法执行") {
		t.Errorf("err=%q, want contain 'leg granularity unexecutable'（红=现码旧文案 leg notional imbalance）", err)
	}
}

// TestD329_MeanRevertWithCap_SameHelper T4：MeanRevertWithCap 同源修锚——双腿失衡
// （T3 同构 fixture）经 ExecuteMeanRevertWithCap 返回新错误串，证明失衡块走 matchLegsNotional。
// DSN-gated（checkAutotradeState 需 RUNNING 行；helper 在 pipeline/liveGate 之前返回）。
func TestD329_MeanRevertWithCap_SameHelper(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert RUNNING: %v", err)
	}
	now := time.Now()
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vA", VenueType: "mt5", Bid: 999, Ask: 1000, Time: now, VenueSymbol: "A1"})
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vB", VenueType: "mt5", Bid: 1000, Ask: 1000.2, Time: now, VenueSymbol: "B1"})
	pipeline := exec.NewPipeline(exec.NewRegistry(), st, qb, exec.RiskLimits{}, "demo")
	s := &Service{store: st, qbus: qb, pipeline: pipeline}
	// D-466：binance step 只认运行时表（fallback 已撤）。
	sm := NewCexStepMeta("http://unused", 0)
	sm.rt = map[string]float64{"BTCUSDT": 0.001}
	s.stepMeta = sm
	s.SetLegContractSize(func(venueKey, venueSymbol string) (float64, bool) {
		if venueKey == "vA:mt5" {
			return 5, true
		}
		if venueKey == "vB:mt5" {
			return 4.8, true
		}
		return 0, false
	})
	s.legVolumeStep = func(venueKey, venueSymbol string) (float64, bool) { return 1.0, true }
	// maxNotional=1e9：clamp 不触发（T4 只验失衡块同源，独立逻辑不掺入）
	status, msg, _ := s.ExecuteMeanRevertWithCap(ctx, "XAUUSD", "vA:mt5", "vB:mt5", "req-d329", 1, 0, 1e9, "MEANREVERT")
	if status != "FAILED" {
		t.Fatalf("双粗腿应 FAILED, got %s", status)
	}
	if !strings.Contains(msg, "腿粒度无法执行") {
		t.Errorf("msg=%q, want contain 'leg granularity unexecutable'（红=现码旧文案）", msg)
	}
}

// TestD329R1_MeanRevertAsymmetricClampImbalanceRecheck R3 返修锚：非对称 clamp——
// MT 买腿超帽压回 1 step + CEX 卖腿不 clamp → clamp 后失衡复检拦回（FAILED 含原文案
// "腿名义额失衡"）。红=现码（R1 前）helper 匹配平衡放行穿透过 clamp、后续门禁
// 以别的原因 FAILED。数值：qty=0.05，helper 匹配平衡（买 0.05×100×4355=$21775=卖
// 5×1×4355）；clamp 后买=0.01（$4355）vs 卖 5（$21775）→ imb=400%。
func TestD329R1_MeanRevertAsymmetricClampImbalanceRecheck(t *testing.T) {
	st := serviceTestStoreSingleInit(t)
	ctx := context.Background()
	if _, err := st.Pool().Exec(ctx, `TRUNCATE autotrade_state CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := st.Pool().Exec(ctx, `INSERT INTO autotrade_state (state, reason, halted_on) VALUES ('RUNNING', '', NULL)`); err != nil {
		t.Fatalf("insert RUNNING: %v", err)
	}
	now := time.Now()
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "vA", VenueType: "mt5", Bid: 4354.5, Ask: 4355, Time: now, VenueSymbol: "XAUUSDm"})
	qb.Publish(bus.Quote{Symbol: "XAUUSD", Venue: "binance", VenueType: "perp", Bid: 4355, Ask: 4355.2, Time: now, VenueSymbol: "BTCUSDT"})
	pipeline := exec.NewPipeline(exec.NewRegistry(), st, qb, exec.RiskLimits{}, "demo")
	s := &Service{store: st, qbus: qb, pipeline: pipeline}
	// D-466：binance step 只认运行时表（fallback 已撤）。
	sm := NewCexStepMeta("http://unused", 0)
	sm.rt = map[string]float64{"BTCUSDT": 0.001}
	s.stepMeta = sm
	s.SetLegContractSize(func(venueKey, venueSymbol string) (float64, bool) {
		if venueKey == "vA:mt5" {
			return 100, true // CS=100oz/lot
		}
		return 0, false
	})
	s.SetCexContractSize(func(venue, venueSymbol string) (float64, bool) {
		if venue == "binance" && venueSymbol == "BTCUSDT" {
			return 1.0, true
		}
		return 0, false
	})
	// maxNotional=1000：买腿 $21775 超帽 → 压回 1 step；CEX 卖腿无 clamp。
	status, msg, _ := s.ExecuteMeanRevertWithCap(ctx, "XAUUSD", "vA:mt5", "binance:perp", "req-d329r1", 0.05, 0, 1000, "MEANREVERT")
	if status != "FAILED" {
		t.Fatalf("非对称 clamp 后应 FAILED, got %s", status)
	}
	if !strings.Contains(msg, "腿名义额失衡") {
		t.Errorf("msg=%q, want contain 'leg notional imbalance'（红=R1 前放行穿透过，后续门禁以别的原因拒）", msg)
	}
}
