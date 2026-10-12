package exec

// D-010 S2/S3 锚测：capGuardInputs margin 口径——margin_usd 预算→单腿名义
// N=margin÷(fracB+fracS)（canonical proposed=N 名义口径）+ 本仓腿 venue→
// 计划保证金 map（N×frac_leg，同 venue 双腿累加）；frac 实读缺一 → fail-closed。

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

func d010Pipeline(t *testing.T, marginUSD *float64, fracFor func(ctx context.Context, vk, sym string, sell bool) (float64, bool)) (*Pipeline, string) {
	t.Helper()
	s := d311ExecStore(t)
	ctx := context.Background()
	oppID := "XASSET_CARRY:WTIUSD:3001157:mt5:gate:perp"
	cs := "BTCUSDT"
	if _, err := s.Pool().Exec(ctx, `TRUNCATE opportunities CASCADE`); err != nil {
		t.Fatalf("truncate: %v", err)
	}
	if _, err := s.UpsertOpportunity(ctx, &pgstore.Opportunity{
		ID: oppID, Type: "XASSET_CARRY", Canonical: "WTIUSD", CanonicalSell: &cs,
		VenueBuy: "3001157:mt5", VenueSell: "gate:perp",
		QuoteTime: time.Now(), ExpiresAt: time.Now().Add(time.Hour),
	}); err != nil {
		t.Fatalf("seed opportunity: %v", err)
	}
	p := &Pipeline{store: s}
	p.SetXAssetComboAllow([]XAssetComboAllowSpec{{
		CanonicalBuy: "WTIUSD", CanonicalSell: "BTCUSDT",
		VenueBuy: "3001157:mt5", VenueSell: "gate:perp",
		MaxLongQty: 1, MaxShortQty: 1000, MaxGrossUSD: 50000, MarginUSD: marginUSD,
	}})
	p.SetMarginInputs(nil, fracFor)
	return p, oppID
}

func d010Legs() []LegSpec {
	return []LegSpec{
		{VenueKey: "3001157:mt5", VenueSymbol: "WTIUSD", Side: "BUY", Qty: 0.5, PriceExpected: 40},
		{VenueKey: "gate:perp", VenueSymbol: "BTC_USDT", Side: "SELL", Qty: 4, PriceExpected: 100000},
	}
}

// margin 模式：fracB=0.02 fracS=0.1 → N=120/0.12=1000；venue 计划保证金=20/100。
func TestD010_CapGuardMarginInputs(t *testing.T) {
	m := 120.0
	p, oppID := d010Pipeline(t, &m, func(_ context.Context, vk, _ string, sell bool) (float64, bool) {
		if sell {
			return 0.1, true
		}
		return 0.02, true
	})
	canonicals, margins, proposed, err := p.capGuardInputs(context.Background(), "XASSET_CARRY", oppID, d010Legs())
	if err != nil {
		t.Fatalf("capGuardInputs: %v", err)
	}
	if len(canonicals) != 2 || canonicals[0] != "WTIUSD" || canonicals[1] != "BTCUSDT" {
		t.Fatalf("canonicals=%v", canonicals)
	}
	if math.Abs(proposed-1000) > 1e-6 {
		t.Fatalf("margin 反推单腿名义应为 1000, got %v", proposed)
	}
	if math.Abs(margins["3001157:mt5"]-20) > 1e-6 || math.Abs(margins["gate:perp"]-100) > 1e-6 {
		t.Fatalf("腿计划保证金应为 20/100, got %+v", margins)
	}
}

// 名义模式：notional_usd=单腿名义口径（不取半）；frac 缺→该腿按全款计。
func TestD010_CapGuardNotionalInputs(t *testing.T) {
	p, oppID := d010Pipeline(t, nil, func(context.Context, string, string, bool) (float64, bool) {
		return 0, false // 全部 unknown → 名义全款
	})
	p.xassetAllowSpecs()[0].NotionalUSD = ptrF(4000)
	_, margins, proposed, err := p.capGuardInputs(context.Background(), "XASSET_CARRY", oppID, d010Legs())
	if err != nil {
		t.Fatalf("capGuardInputs: %v", err)
	}
	if proposed != 4000 || margins["3001157:mt5"] != 4000 || margins["gate:perp"] != 4000 {
		t.Fatalf("notional 单腿口径 proposed=4000、frac 缺→全款: %v %+v", proposed, margins)
	}
}

// margin 模式 frac 缺任一 → fail-closed 拒（杠杆实读失败保证金不可折算）。
func TestD010_CapGuardMarginFailClosed(t *testing.T) {
	m := 120.0
	p, oppID := d010Pipeline(t, &m, func(_ context.Context, _ string, _ string, sell bool) (float64, bool) {
		return 0.02, !sell // 卖腿杠杆读败
	})
	_, _, _, err := p.capGuardInputs(context.Background(), "XASSET_CARRY", oppID, d010Legs())
	if err == nil || !strings.Contains(err.Error(), "保证金") {
		t.Fatalf("frac 缺一应 fail-closed 拒, got %v", err)
	}
}

func ptrF(v float64) *float64 { return &v }
