package dashboard

// D-010 锚测：S0 qty 闸 2× 误拦回归（notional_usd=单腿名义，4362≥4355 放行）；
// S1 margin_usd 与 notional_usd 互斥/非正拒；S3 margin→单腿名义反推
// （不等杠杆→不等腿保证金，N=margin÷(fracB+fracS)）+ 杠杆读败 fail-closed。

import (
	"context"
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// d010Svc 腿数据缝全接线（qbus 报价+合约单位+步进+杠杆实读/fact）：
// MT 买腿 XPTUSD@3001157:mt5 minStep 名义=0.01×(43.55×10000)=$4355；
// CEX 卖腿 XPTUSDT@gate:perp minStep 名义=1×(4355×1)=$4355 → 组合 minN=$4355。
// MT frac=init_margin_rate fact 0.05（≥悲观档下限）；perp frac=杠杆实读 10x→0.1。
func d010Svc(t *testing.T) *Service {
	t.Helper()
	s := &Service{}
	qb := bus.New()
	qb.Publish(bus.Quote{Symbol: "XPTUSD", Venue: "3001157", VenueType: "mt5",
		Bid: 43.55, Ask: 43.55, VenueSymbol: "XPTUSDm", Time: time.Now()})
	qb.Publish(bus.Quote{Symbol: "XPTUSDT", Venue: "gate", VenueType: "perp",
		Bid: 4355, Ask: 4355.1, VenueSymbol: "XPT_USDT", Time: time.Now()})
	s.qbus = qb
	s.SetLegUnitQty(func(string, string) (float64, bool) { return 10000, true })
	s.cexContractSize = func(string, string) (float64, bool) { return 1, true }
	sm := NewCexStepMeta("", time.Hour)
	sm.rt = map[string]float64{"XPT_USDT": 1}
	s.SetStepMeta(sm)
	f := &fakeGradDB{}
	f.facts = map[string][]fact.Fact{
		"3001157|": {{Kind: fact.KindInitMarginRate, Venue: "3001157", Value: 0.05, Ts: time.Now()}},
	}
	s.gradStore = f
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) { return 10, nil }
	return s
}

func d010Combo(notional, margin *float64) pgstore.XAssetComboAdminRow {
	return pgstore.XAssetComboAdminRow{
		CanonicalBuy: "XPTUSD", CanonicalSell: "XPTUSDT",
		VenueBuy: "3001157:mt5", VenueSell: "gate:perp",
		NotionalUSD: notional, MarginUSD: margin,
	}
}

// S0：per-leg 预算 4362 ≥ 两腿 min $4355 → 放行（旧 2×max 判据误拦回归锚）。
func TestD010_QtyGateS0(t *testing.T) {
	s := d010Svc(t)
	ctx := context.Background()
	n4362 := 4362.0
	if msg, blocked := s.comboQtyBelowMin(ctx, d010Combo(&n4362, nil)); blocked {
		t.Fatalf("S0 回归：4362≥4355 应放行（2× 误拦已修）, got %q", msg)
	}
	n4000 := 4000.0
	msg, blocked := s.comboQtyBelowMin(ctx, d010Combo(&n4000, nil))
	if !blocked || !strings.Contains(msg, "$4355") || !strings.Contains(msg, "$4000") {
		t.Fatalf("低于最小应拦且文案带 min/当前/差值, got blocked=%v %q", blocked, msg)
	}
}

// S3：margin 预算 → 单腿名义 N=margin÷(fracB+fracS)；不等杠杆→不等腿保证金。
// fracB=0.05（MT fact）、fracS=0.1（perp 10x）→ N=1500/0.15=10000，腿占 500/1000。
func TestD010_MarginDerivation(t *testing.T) {
	s := d010Svc(t)
	ctx := context.Background()
	m1500 := 1500.0
	c := d010Combo(nil, &m1500)
	n, fB, fS, kB, kS, ok := s.comboLegNotional(ctx, c)
	if !ok || !kB || !kS {
		t.Fatalf("双腿杠杆实读应 ok: n=%v fB=%v fS=%v", n, fB, fS)
	}
	if math.Abs(n-10000) > 1e-6 || fB != 0.05 || fS != 0.1 {
		t.Fatalf("N=margin÷(fracB+fracS) 应为 10000（腿占 500/1000）, got n=%v fB=%v fS=%v", n, fB, fS)
	}
	// margin 充足（1500≥minMargin 653.25）→ 放行。
	if msg, blocked := s.comboQtyBelowMin(ctx, c); blocked {
		t.Fatalf("margin 1500≥653 应放行, got %q", msg)
	}
	// margin 不足 → 文案「最少需保证金 $653 / 当前 $600 / 差」。
	m600 := 600.0
	msg, blocked := s.comboQtyBelowMin(ctx, d010Combo(nil, &m600))
	if !blocked || !strings.Contains(msg, "保证金") || !strings.Contains(msg, "$653") {
		t.Fatalf("margin 不足应拦且文案带保证金口径, got blocked=%v %q", blocked, msg)
	}
	// 杠杆实读失败 → fail-closed 拦（不猜杠杆）。
	s.venueLeverageFn = func(context.Context, string, string, bool) (float64, error) {
		return 0, errors.New("venue leverage read failed")
	}
	s.levCache = nil // 清 5min 读穿缓存（上段已缓存 lev=10）
	if _, _, _, _, _, ok := s.comboLegNotional(ctx, c); ok {
		t.Fatal("杠杆读败应 fail-closed")
	}
	if msg, blocked := s.comboQtyBelowMin(ctx, c); !blocked || !strings.Contains(msg, "杠杆实读失败") {
		t.Fatalf("杠杆读败应拦且带因, got blocked=%v %q", blocked, msg)
	}
}

// S1：margin_usd 与 notional_usd 互斥 + 显式非正值拒（RPC 校验前置，不触库）。
func TestD010_MarginMutex(t *testing.T) {
	s := &Service{xassetReloader: &fakeReloader{}}
	mk := func(notional, margin *float64) *connect.Request[v1.UpsertXAssetAllowComboRequest] {
		return connect.NewRequest(&v1.UpsertXAssetAllowComboRequest{
			Id: 1, CanonicalBuy: "XPTUSD", CanonicalSell: "XPTUSDT",
			VenueBuy: "3001157:mt5", VenueSell: "gate:perp",
			MaxLongQty: 1, MaxShortQty: 1, MaxGrossUsd: 10000,
			NotionalUsd: notional, MarginUsd: margin, Enabled: true,
		})
	}
	n, m := 100.0, 100.0
	resp, err := s.UpsertXAssetAllowCombo(context.Background(), mk(&n, &m))
	if err != nil || !strings.Contains(resp.Msg.Error, "互斥") {
		t.Fatalf("双口径并存应拒（互斥）, got err=%v resp=%+v", err, resp.Msg)
	}
	m0 := -1.0
	resp, err = s.UpsertXAssetAllowCombo(context.Background(), mk(nil, &m0))
	if err != nil || !strings.Contains(resp.Msg.Error, "禁非正") {
		t.Fatalf("margin 非正应拒, got err=%v resp=%+v", err, resp.Msg)
	}
}
