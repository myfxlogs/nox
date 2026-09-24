package dashboard

// D-466 T1-T3：腿级 venue 原生结算时刻——跨 venue 污染复现红→绿+三级回退+gate 原生字段。
import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/collect/exchange"
	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"arb/internal/exec"
	"arb/internal/reconcile"
)

func d466Intervals(t *testing.T, ctx context.Context) *exchange.FundingIntervalCache {
	t.Helper()
	fiSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// binance 自家 XAUUSDT=4h 期——污染源（与 OKX 同名不同期，生产实证）。
		_ = json.NewEncoder(w).Encode([]map[string]any{{"symbol": "XAUUSDT", "fundingIntervalHours": 4}})
	}))
	t.Cleanup(fiSrv.Close)
	c := exchange.NewFundingIntervalCache()
	if err := c.Populate(ctx, exchange.Config{BinanceBaseURL: fiSrv.URL, Client: fiSrv.Client()}); err != nil {
		t.Fatalf("populate: %v", err)
	}
	return c
}

func d466Mon(q *fakeCEXQuerier, intervals *exchange.FundingIntervalCache,
	settle func(context.Context, string, string) (time.Time, time.Time, time.Duration)) *BrokerPositionMonitor {
	qb := bus.New()
	bm := NewBrokerManager(nil, qb, exec.NewRegistry(), time.Minute)
	bm.AddStaticQuerier(q.key, q)
	return NewBrokerPositionMonitor(bm, qb, time.Minute, time.Millisecond, time.Second,
		bus.NewTopic[*v1.BrokerPositionSnapshot](), NewServerTZCache(time.Minute, func(context.Context, string) (float64, error) {
			return 0, errors.New("n/a")
		}), intervals, settle)
}

// T1：OKX 腿 + binance 缓存同名 XAUUSDT(4h) → swapChargeAt=OKX venue 原生 next_settle
// （非 binance 4h 边界）——pos502 倒计时 bug 确定性复现。
func TestD466_OKXLegVenueNativeSettle(t *testing.T) {
	ctx := context.Background()
	intervals := d466Intervals(t, ctx)
	okxNext := time.Now().UTC().Truncate(time.Second).Add(7*time.Hour + 40*time.Minute) // OKX 8h 边界时刻
	q := &fakeCEXQuerier{key: "okx:perp", orders: []reconcile.VenueOrder{
		{Venue: "okx", VenueKey: "okx:perp", Ticket: "XAU-USDT-SWAP", Symbol: "XAU-USDT-SWAP", Side: "SELL", Volume: 1016, OpenPrice: 4297, Profit: 0},
	}}
	mon := d466Mon(q, intervals, func(_ context.Context, venue, symbol string) (time.Time, time.Time, time.Duration) {
		if venue != "okx" || symbol != "XAUUSDT" {
			return time.Time{}, time.Time{}, 0
		}
		return okxNext, time.Now(), 8 * time.Hour
	})
	got := mon.Refresh(ctx)
	if len(got) != 1 || got[0].SwapChargeAt == nil {
		t.Fatalf("want 1 leg with settle, got %+v", got)
	}
	if !got[0].SwapChargeAt.AsTime().Equal(okxNext) {
		t.Fatalf("T1 swapChargeAt=%v want OKX 原生 %v（红=binance 4h 边界 %v）",
			got[0].SwapChargeAt.AsTime(), okxNext, NextFundingSettlementUTC(time.Now().UTC(), 6))
	}
}

// T2（R1 终态=nil 禁 8h 兜底）：①无 fact 非 binance 腿→nil 空态+warn（不吃 binance
// 4h 表=污染回归锚）；②stale fact（龄>2×周期）退 venue interval 边界；③binance 腿
// 无 fact 用自家缓存 4h；④binance 缓存 miss→nil 空态。
func TestD466_SettleFallbackChain(t *testing.T) {
	ctx := context.Background()
	intervals := d466Intervals(t, ctx)
	now := time.Now().UTC()

	// ① okx 腿零 fact → nil 空态+warn（R1 禁 8h 兜底；旧码给 binance 4h 边界=污染）
	q := &fakeCEXQuerier{key: "okx:perp", orders: []reconcile.VenueOrder{
		{Venue: "okx", VenueKey: "okx:perp", Ticket: "T", Symbol: "XAU-USDT-SWAP", Side: "SELL", Volume: 1},
	}}
	mon := d466Mon(q, intervals, func(context.Context, string, string) (time.Time, time.Time, time.Duration) {
		return time.Time{}, time.Time{}, 0
	})
	got := mon.Refresh(ctx)
	if got[0].SwapChargeAt != nil {
		t.Fatalf("T2a okx 无 fact want nil 空态, got %v（污染=4h 边界 %v；旧兜底=8h %v）",
			got[0].SwapChargeAt.AsTime(), NextFundingSettlementUTC(now, 6), NextFundingSettlementUTC(now, 3))
	}
	if got[0].FundingRatePeriodicPct != 0 {
		t.Fatalf("T2a R2 periodicPct 非原生来源 want 0, got %v", got[0].FundingRatePeriodicPct)
	}

	// ② stale fact（ts=now-17h > 2×8h）+ interval=8h → venue interval 边界（8h）；
	// 用 4h interval 区分：fact stale + ivl=4h → 4h 边界而非 next 值
	next := now.Add(90 * time.Minute)
	mon2 := d466Mon(q, intervals, func(context.Context, string, string) (time.Time, time.Time, time.Duration) {
		return next, now.Add(-9 * time.Hour), 4 * time.Hour // 龄 9h > 2×4h=stale
	})
	got2 := mon2.Refresh(ctx)
	want4 := NextFundingSettlementUTC(now, 6)
	if got2[0].SwapChargeAt == nil || got2[0].SwapChargeAt.AsTime().Sub(want4).Abs() > 5*time.Second {
		t.Fatalf("T2b stale fact want venue interval 4h 边界 %v±5s, got %v", want4, got2[0].SwapChargeAt.AsTime())
	}

	// ③ binance 腿无 fact → 自家 intervals 4h（venue 原生合法回退）
	qb := &fakeCEXQuerier{key: "binance:perp", orders: []reconcile.VenueOrder{
		{Venue: "binance", VenueKey: "binance:perp", Ticket: "T", Symbol: "XAUUSDT", Side: "SELL", Volume: 1},
	}}
	mon3 := d466Mon(qb, intervals, func(context.Context, string, string) (time.Time, time.Time, time.Duration) {
		return time.Time{}, time.Time{}, 0
	})
	got3 := mon3.Refresh(ctx)
	if got3[0].SwapChargeAt == nil || got3[0].SwapChargeAt.AsTime().Sub(want4).Abs() > 5*time.Second {
		t.Fatalf("T2c binance 无 fact want 自家 4h 边界 %v±5s, got %v", want4, got3[0].SwapChargeAt.AsTime())
	}

	// ④ binance 腿缓存 miss → nil 空态（自家表亦无=诚实缺，不猜）
	qc := &fakeCEXQuerier{key: "binance:perp", orders: []reconcile.VenueOrder{
		{Venue: "binance", VenueKey: "binance:perp", Ticket: "T", Symbol: "ZZZUSDT", Side: "SELL", Volume: 1},
	}}
	mon4 := d466Mon(qc, intervals, func(context.Context, string, string) (time.Time, time.Time, time.Duration) {
		return time.Time{}, time.Time{}, 0
	})
	got4 := mon4.Refresh(ctx)
	if got4[0].SwapChargeAt != nil {
		t.Fatalf("T2d binance 缓存 miss want nil, got %v", got4[0].SwapChargeAt.AsTime())
	}
}

// T3：gate 腿用自家 funding_next_apply fact，不误取 binance 值。
func TestD466_GateLegVenueNative(t *testing.T) {
	ctx := context.Background()
	intervals := d466Intervals(t, ctx)
	gateNext := time.Now().UTC().Truncate(time.Second).Add(2 * time.Hour)
	q := &fakeCEXQuerier{key: "gate:perp", orders: []reconcile.VenueOrder{
		{Venue: "gate", VenueKey: "gate:perp", Ticket: "T", Symbol: "XAU_USDT", Side: "SELL", Volume: 10},
	}}
	mon := d466Mon(q, intervals, func(_ context.Context, venue, symbol string) (time.Time, time.Time, time.Duration) {
		if venue != "gate" || symbol != "XAUUSDT" {
			return time.Time{}, time.Time{}, 0
		}
		return gateNext, time.Now(), 4 * time.Hour
	})
	got := mon.Refresh(ctx)
	if len(got) != 1 || got[0].SwapChargeAt == nil || !got[0].SwapChargeAt.AsTime().Equal(gateNext) {
		t.Fatalf("T3 gate swapChargeAt=%v want 自家 %v", got[0].SwapChargeAt.AsTime(), gateNext)
	}
}
