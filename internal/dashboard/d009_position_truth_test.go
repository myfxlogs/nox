package dashboard

// D-009 持仓面真实性测试锚点：
// S1 CEX 注册 kick / S2 双价+方向价 / S3 平仓净估 / S4 qty 明示闸。

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arb/internal/bus"
	"arb/internal/collect/exchange"
	"arb/internal/exec"
	"arb/internal/store/pgstore"
	"arb/internal/venue"

	v1 "arb/internal/gen/dashboardv1"
)

// ---------- S1：CEX perp querier 新注册 → bpMon 收到 kick ----------

func TestD009_S1_NewQuerierKick(t *testing.T) {
	ctx := context.Background()
	qbus := bus.New()
	bm := NewBrokerManager(nil, qbus, exec.NewRegistry(), time.Minute)
	mon := NewBrokerPositionMonitor(bm, qbus, time.Minute, nil,
		NewServerTZCache(time.Minute, func(context.Context, string) (float64, error) { return 0, errors.New("n/a") }),
		exchange.NewFundingIntervalCache(), nil)
	bm.SetBPM(mon)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer srv.Close()
	fake := &fakeExchangeStore{cfgs: []pgstore.ExchangeConfig{
		{ID: 1, Venue: "binance", VenueType: "perp", APIKey: "k", APISecret: "s",
			BaseURL: srv.URL, Simulated: false},
	}}
	pm := NewCexPositionManager(bm, fake, time.Hour)
	pm.sync(ctx)
	mon.mu.Lock()
	_, kicked := mon.pendingKick["binance:perp"]
	mon.mu.Unlock()
	if !kicked {
		t.Fatal("新注册 binance:perp 应 kick bpMon（boot 竞态叫醒）")
	}
	// 二轮 sync 无新键 → pendingKick 不重复增长（消费后不再驻留）。
	delete(mon.pendingKick, "binance:perp")
	pm.sync(ctx)
	mon.mu.Lock()
	defer mon.mu.Unlock()
	if len(mon.pendingKick) != 0 {
		t.Fatalf("二轮 sync 无新注册不应再 kick, pendingKick=%v", mon.pendingKick)
	}
}

// ---------- S2：direction price + bid/ask ----------

func TestD009_S2_DirectionPrice(t *testing.T) {
	ttl := time.Minute
	now := time.Now()
	price := bus.Quote{Bid: 99.5, Ask: 100.5, Time: now}
	mk := func(side string) venue.Order {
		return venue.Order{Venue: "b", VenueKey: "b:perp", Ticket: "1", Symbol: "X", Side: side, Volume: 1, OpenPrice: 100}
	}
	buy := brokerPositionToProto(mk("BUY"), "X", price, true, true, nil, ttl, now, 0, nil, 0)
	if buy.CurrentPrice != 99.5 {
		t.Fatalf("BUY 仓平仓价应=bid 99.5, got %v", buy.CurrentPrice)
	}
	if buy.PriceBid != 99.5 || buy.PriceAsk != 100.5 {
		t.Fatalf("双价应齐填 99.5/100.5, got %v/%v", buy.PriceBid, buy.PriceAsk)
	}
	sell := brokerPositionToProto(mk("SELL"), "X", price, true, true, nil, ttl, now, 0, nil, 0)
	if sell.CurrentPrice != 100.5 {
		t.Fatalf("SELL 仓平仓价应=ask 100.5, got %v", sell.CurrentPrice)
	}
	// D-162 ②：stale 价照填+price_at 真钟（休市末价诚实展示），fresh=false 如实。
	stale := brokerPositionToProto(mk("BUY"), "X", bus.Quote{Bid: 1, Ask: 2, Time: now.Add(-time.Hour)}, true, true, nil, ttl, now, 0, nil, 0)
	if stale.Fresh || stale.CurrentPrice != 1 || stale.PriceBid != 1 || stale.PriceAt == nil {
		t.Fatalf("超龄报价应 stale 照显（fresh=false+price_at）, got %+v", stale)
	}
}

// ---------- S3：平仓净估公式 + 缺输入诚实空态 ----------

func TestD009_S3_CloseEstimate(t *testing.T) {
	svc := &Service{qbus: bus.New()}
	svc.legContractSize = func(string, string) (float64, bool) { return 2, true } // MT 原生 CS=2
	svc.commissionFor = func(string) float64 { return 5 }                         // 5 bps
	bp := &v1.BrokerPosition{
		VenueKey: "v:mt5", Symbol: "XAUUSD", Canonical: "XAUUSD", Side: "BUY",
		Volume: 3, OpenPrice: 100, CurrentPrice: 110, Fresh: true,
	}
	// gross=(110-100)*3*2*1=60；名义=3*2*110=660；fee=660*5e-4=0.33 → est=59.67
	est, ok := svc.closeEstimateUSD(context.Background(), bp)
	if !ok {
		t.Fatal("USD 计价全输入应得估值")
	}
	if d := est - 59.67; d > 0.01 || d < -0.01 {
		t.Fatalf("est want ≈59.67, got %v", est)
	}
	// SELL 方向：gross 取反。
	bp.Side = "SELL"
	est2, _ := svc.closeEstimateUSD(context.Background(), bp)
	if est2 >= 0 {
		t.Fatalf("SELL 涨仓应亏, got %v", est2)
	}
	// 缺报价（Fresh=false）→ 诚实空态。
	bp.Fresh = false
	if _, ok := svc.closeEstimateUSD(context.Background(), bp); ok {
		t.Fatal("报价不新鲜应空态")
	}
	// cs 解析缺失 → 空态不猜。
	svc.legContractSize = func(string, string) (float64, bool) { return 0, false }
	bp.Fresh = true
	if _, ok := svc.closeEstimateUSD(context.Background(), bp); ok {
		t.Fatal("cs 不可得应空态")
	}
}

// ---------- S4：qty 明示闸 ----------

func TestD009_S4_QtyMinStepReason(t *testing.T) {
	svc := &Service{qbus: bus.New()}
	svc.legUnitQty = func(string, string) (float64, bool) { return 100, true } // cs=100
	svc.legVolumeStep = func(string, string) (float64, bool) { return 0.01, true }
	pub := func(venue, sym string, bid, ask float64) {
		svc.qbus.Publish(bus.Quote{Symbol: sym, Venue: venue, VenueType: "mt5",
			Bid: bid, Ask: ask, Time: time.Now(), VenueSymbol: sym})
	}
	pub("vA", "XAUUSD", 2400, 2401)
	pub("vB", "XAUUSD", 2400, 2401)
	c := pgstore.XAssetComboAdminRow{
		VenueBuy: "vA:mt5", VenueSell: "vB:mt5",
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSD", MaxGrossUSD: 3000,
	}
	// D-010 S0：判据=单腿名义（gross/2）≥max(minB,minS)——gross 3000→N=1500<2401 拦。
	reason, blocked := svc.comboQtyBelowMin(context.Background(), c)
	if !blocked {
		t.Fatal("单腿名义预算 1500 < 最小可开 0.01×100×2401=2401 应拦截")
	}
	if !strings.Contains(reason, "最小可开") || !strings.Contains(reason, "2401") || !strings.Contains(reason, "1500") {
		t.Fatalf("waitReason 应含最小可开/当前预算金额, got %q", reason)
	}
	// 预算够 → 放行（gross 6000→N=3000≥2401）。
	c.MaxGrossUSD = 6000
	if _, blocked := svc.comboQtyBelowMin(context.Background(), c); blocked {
		t.Fatal("单腿名义预算 3000 ≥ 2401 应放行")
	}
	// 无报价 → 不阻塞（其余闸各自把守）。
	c2 := pgstore.XAssetComboAdminRow{VenueBuy: "vX:mt5", VenueSell: "vB:mt5",
		CanonicalBuy: "XAUUSD", CanonicalSell: "XAUUSD", MaxGrossUSD: 1}
	if _, blocked := svc.comboQtyBelowMin(context.Background(), c2); blocked {
		t.Fatal("无报价输入不应阻塞")
	}
}
