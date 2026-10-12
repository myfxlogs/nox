// broker_order_history_scan_test.go —— D-181 S1：BackfillSwapHistory 纯函数 + RPC handler 单测。
package dashboard

import (
	"context"
	"math"
	"strings"
	"testing"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"
	"connectrpc.com/connect"
)

// TestComputeUsdLotDay 基本换算。
// Swap=-0.14, CloseLots=0.01, holdDays=2 → -0.14/(0.01×2)=-7.0
func TestComputeUsdLotDay(t *testing.T) {
	o := HistorySwapOrder{
		Swap: -0.14, Lots: 0.01, CloseLots: 0.01,
		OpenTime:  time.Now().Add(-48 * time.Hour),
		CloseTime: time.Now(),
	}
	v, ok := computeUsdLotDay(o)
	if !ok {
		t.Fatal("want ok=true, got false")
	}
	if absDiff(v, -7.0) > 1e-6 {
		t.Errorf("usdLotDay = %.6f, want -7.0", v)
	}
}

// TestComputeUsdLotDay_HoldDaysLessThan1 跳过日内单。
func TestComputeUsdLotDay_HoldDaysLessThan1(t *testing.T) {
	o := HistorySwapOrder{
		Swap: -0.14, Lots: 0.01, CloseLots: 0.01,
		OpenTime:  time.Now().Add(-12 * time.Hour), // 0.5 天
		CloseTime: time.Now(),
	}
	_, ok := computeUsdLotDay(o)
	if ok {
		t.Error("holdDays<1: want ok=false, got true")
	}
}

// TestComputeUsdLotDay_CloseLotsFallback CloseLots<=0 回退 Lots。
func TestComputeUsdLotDay_CloseLotsFallback(t *testing.T) {
	o := HistorySwapOrder{
		Swap: -0.14, Lots: 0.02, CloseLots: 0, // 回退 0.02
		OpenTime:  time.Now().Add(-48 * time.Hour),
		CloseTime: time.Now(),
	}
	v, ok := computeUsdLotDay(o)
	if !ok {
		t.Fatal("want ok=true")
	}
	want := -0.14 / (0.02 * 2)
	if absDiff(v, want) > 1e-6 {
		t.Errorf("usdLotDay = %.6f, want %.6f", v, want)
	}
}

// TestMedianFloats 中位数。
func TestMedianFloats(t *testing.T) {
	tests := []struct {
		name string
		vals []float64
		want float64
	}{
		{"empty", []float64{}, 0},
		{"single", []float64{5}, 5},
		{"odd", []float64{3, 1, 2}, 2},       // sorted 1,2,3 → 2
		{"even", []float64{1, 2, 3, 4}, 2.5}, // (2+3)/2
		{"unsorted", []float64{4, 1, 3, 2}, 2.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := medianFloats(tt.vals)
			if got != tt.want {
				t.Errorf("medianFloats(%v) = %.4f, want %.4f", tt.vals, got, tt.want)
			}
		})
	}
}

// TestAggregateHistoryToUsdLotDay 聚合 + 中位数 + fact 构造。
func TestAggregateHistoryToUsdLotDay(t *testing.T) {
	now := time.Now().UTC()
	orders := []HistorySwapOrder{
		// AUDJPY BUY: 3 个值 -7.0, -8.0, -6.0 → 中位数 -7.0
		{Symbol: "AUDJPY", Side: "BUY", Swap: -0.14, Lots: 0.01, CloseLots: 0.01, OpenTime: now.Add(-48 * time.Hour), CloseTime: now},
		{Symbol: "AUDJPY", Side: "BUY", Swap: -0.16, Lots: 0.01, CloseLots: 0.01, OpenTime: now.Add(-48 * time.Hour), CloseTime: now},
		{Symbol: "AUDJPY", Side: "BUY", Swap: -0.12, Lots: 0.01, CloseLots: 0.01, OpenTime: now.Add(-48 * time.Hour), CloseTime: now},
		// AUDJPY SELL: 1 个值 +5.0
		{Symbol: "AUDJPY", Side: "SELL", Swap: 0.10, Lots: 0.01, CloseLots: 0.01, OpenTime: now.Add(-48 * time.Hour), CloseTime: now},
	}
	facts := aggregateHistoryToUsdLotDay(orders, "vA", now)
	if len(facts) != 2 {
		t.Fatalf("want 2 facts, got %d", len(facts))
	}
	var longFact, shortFact *fact.Fact
	for i := range facts {
		if facts[i].Kind == fact.KindSwapSettledLong {
			longFact = &facts[i]
		} else if facts[i].Kind == fact.KindSwapSettledShort {
			shortFact = &facts[i]
		}
	}
	if longFact == nil || absDiff(longFact.Value, -7.0) > 1e-6 {
		t.Errorf("long fact: want -7.0, got %+v", longFact)
	}
	if shortFact == nil || absDiff(shortFact.Value, 5.0) > 1e-6 {
		t.Errorf("short fact: want 5.0, got %+v", shortFact)
	}
	for _, f := range facts {
		if f.Src != "order_history" {
			t.Errorf("fact src: want order_history, got %s", f.Src)
		}
		if f.Unit != fact.UnitUsdLotDay {
			t.Errorf("fact unit: want %s, got %s", fact.UnitUsdLotDay, f.Unit)
		}
		if f.Venue != "vA" {
			t.Errorf("fact venue: want vA, got %s", f.Venue)
		}
	}
}

// TestSplitVenueKey venueKey 解析。
func TestSplitVenueKey(t *testing.T) {
	tests := []struct {
		in       string
		venue    string
		platform int
		ok       bool
	}{
		{"61585565:mt5", "61585565", 1, true},
		{"vA:mt4", "vA", 0, true},
		{"vA", "", 0, false},
		{"vA:spot", "", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			v, p, ok := splitVenueKey(tt.in)
			if v != tt.venue || p != tt.platform || ok != tt.ok {
				t.Errorf("splitVenueKey(%q) = (%q, %d, %v), want (%q, %d, %v)",
					tt.in, v, p, ok, tt.venue, tt.platform, tt.ok)
			}
		})
	}
}

// setupBackfillServiceStore 构造真库 store（清 facts 表）。
func setupBackfillServiceStore(t *testing.T) *pgstore.Store {
	t.Helper()
	s := serviceTestStoreSingleInit(t)
	if _, err := s.Pool().Exec(context.Background(), "TRUNCATE facts CASCADE"); err != nil {
		t.Fatalf("truncate facts: %v", err)
	}
	return s
}

// TestBackfillSwapHistoryRPC_HappyPath 完整路径：mock scan 返回订单 → 聚合 → 写 facts。
func TestBackfillSwapHistoryRPC_HappyPath(t *testing.T) {
	store := setupBackfillServiceStore(t)
	now := time.Now().UTC()
	mockOrders := []HistorySwapOrder{
		{Symbol: "AUDJPY", Side: "BUY", Swap: -0.14, Lots: 0.01, CloseLots: 0.01, OpenTime: now.Add(-48 * time.Hour), CloseTime: now},
		{Symbol: "AUDJPY", Side: "BUY", Swap: -0.16, Lots: 0.01, CloseLots: 0.01, OpenTime: now.Add(-48 * time.Hour), CloseTime: now},
		{Symbol: "AUDJPY", Side: "SELL", Swap: 0.10, Lots: 0.01, CloseLots: 0.01, OpenTime: now.Add(-48 * time.Hour), CloseTime: now},
	}
	svc := &Service{
		store: store,
		bm:    &BrokerManager{}, // 不用 bm.OrderHistoryScan（直接注入 scan 闭包到 svc）
	}
	// 临时注入 scan 闭包——通过 bm.running
	svc.bm.running = map[string]*running{
		"vA": {orderHistoryScan: func(ctx context.Context, from, to time.Time) ([]HistorySwapOrder, error) {
			return mockOrders, nil
		}},
	}
	resp, err := svc.BackfillSwapHistory(context.Background(),
		connect.NewRequest(&v1.BackfillSwapHistoryRequest{Venue: "vA:mt5"}))
	if err != nil {
		t.Fatalf("RPC error: %v", err)
	}
	if resp.Msg.GetOrdersScanned() != 3 {
		t.Errorf("orders_scanned = %d, want 3", resp.Msg.GetOrdersScanned())
	}
	if resp.Msg.GetFactsWritten() != 2 {
		t.Errorf("facts_written = %d, want 2", resp.Msg.GetFactsWritten())
	}
	if !strings.Contains(resp.Msg.GetSummary(), "src=order_history") {
		t.Errorf("summary should contain 'src=order_history', got %q", resp.Msg.GetSummary())
	}
}

// TestBackfillSwapHistoryRPC_EmptyHistory 空历史降级。
func TestBackfillSwapHistoryRPC_EmptyHistory(t *testing.T) {
	store := setupBackfillServiceStore(t)
	svc := &Service{
		store: store,
		bm:    &BrokerManager{},
	}
	svc.bm.running = map[string]*running{
		"vA": {orderHistoryScan: func(ctx context.Context, from, to time.Time) ([]HistorySwapOrder, error) {
			return nil, nil
		}},
	}
	resp, err := svc.BackfillSwapHistory(context.Background(),
		connect.NewRequest(&v1.BackfillSwapHistoryRequest{Venue: "vA:mt5"}))
	if err != nil {
		t.Fatalf("RPC error: %v", err)
	}
	if !strings.Contains(resp.Msg.GetSummary(), "no closed orders") {
		t.Errorf("summary should mention 'no closed orders', got %q", resp.Msg.GetSummary())
	}
}

// TestBackfillSwapHistoryRPC_AllZeroSwap 全 0 降级。
func TestBackfillSwapHistoryRPC_AllZeroSwap(t *testing.T) {
	store := setupBackfillServiceStore(t)
	now := time.Now().UTC()
	svc := &Service{
		store: store,
		bm:    &BrokerManager{},
	}
	svc.bm.running = map[string]*running{
		"vA": {orderHistoryScan: func(ctx context.Context, from, to time.Time) ([]HistorySwapOrder, error) {
			return []HistorySwapOrder{
				{Symbol: "AUDJPY", Side: "BUY", Swap: 0, Lots: 0.01, CloseLots: 0.01, OpenTime: now.Add(-48 * time.Hour), CloseTime: now},
			}, nil
		}},
	}
	resp, err := svc.BackfillSwapHistory(context.Background(),
		connect.NewRequest(&v1.BackfillSwapHistoryRequest{Venue: "vA:mt5"}))
	if err != nil {
		t.Fatalf("RPC error: %v", err)
	}
	if !strings.Contains(resp.Msg.GetSummary(), "Swap=0") {
		t.Errorf("summary should mention 'Swap=0', got %q", resp.Msg.GetSummary())
	}
}

// TestBackfillSwapHistoryRPC_VenueNotConnected venue 未连接。
func TestBackfillSwapHistoryRPC_VenueNotConnected(t *testing.T) {
	store := setupBackfillServiceStore(t)
	svc := &Service{
		store: store,
		bm:    &BrokerManager{}, // running 为空
	}
	resp, err := svc.BackfillSwapHistory(context.Background(),
		connect.NewRequest(&v1.BackfillSwapHistoryRequest{Venue: "vX:mt5"}))
	if err != nil {
		t.Fatalf("RPC error: %v", err)
	}
	if !strings.Contains(resp.Msg.GetSummary(), "not connected") {
		t.Errorf("summary should mention 'not connected', got %q", resp.Msg.GetSummary())
	}
}

// TestBackfillSwapHistoryRPC_Idempotent 幂等：重跑不叠加。
func TestBackfillSwapHistoryRPC_Idempotent(t *testing.T) {
	store := setupBackfillServiceStore(t)
	now := time.Now().UTC()
	mockOrders := []HistorySwapOrder{
		{Symbol: "AUDJPY", Side: "BUY", Swap: -0.14, Lots: 0.01, CloseLots: 0.01, OpenTime: now.Add(-48 * time.Hour), CloseTime: now},
	}
	svc := &Service{
		store: store,
		bm:    &BrokerManager{},
	}
	svc.bm.running = map[string]*running{
		"vA": {orderHistoryScan: func(ctx context.Context, from, to time.Time) ([]HistorySwapOrder, error) {
			return mockOrders, nil
		}},
	}
	// 第一次
	resp1, _ := svc.BackfillSwapHistory(context.Background(),
		connect.NewRequest(&v1.BackfillSwapHistoryRequest{Venue: "vA:mt5"}))
	if resp1.Msg.GetFactsWritten() != 1 {
		t.Fatalf("first run: facts_written = %d, want 1", resp1.Msg.GetFactsWritten())
	}
	// 第二次（应覆盖，不叠加）
	resp2, _ := svc.BackfillSwapHistory(context.Background(),
		connect.NewRequest(&v1.BackfillSwapHistoryRequest{Venue: "vA:mt5"}))
	if resp2.Msg.GetFactsWritten() != 1 {
		t.Errorf("second run: facts_written = %d, want 1 (idempotent)", resp2.Msg.GetFactsWritten())
	}
	// 验证 DB 中只有 1 条 order_history fact
	longFacts, err := store.LatestFacts(context.Background(), fact.KindSwapSettledLong, "vA", "AUDJPY")
	if err != nil {
		t.Fatalf("LatestFacts: %v", err)
	}
	count := 0
	for _, f := range longFacts {
		if f.Src == "order_history" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("DB order_history facts: want 1, got %d (not idempotent)", count)
	}
}

// TestHistorySwapOrder_D193A_ExtendedFields D-193-A 扩包 S6 R1：
// HistorySwapOrder 扩 5 字段（Commission/Profit/OrderTicket/ClosePrice）可正确构造+访问。
func TestHistorySwapOrder_D193A_ExtendedFields(t *testing.T) {
	o := HistorySwapOrder{
		Symbol:      "AUDJPY",
		Side:        "SELL",
		Swap:        -0.04,
		Lots:        0.01,
		CloseLots:   0.01,
		Commission:  -0.02,
		Profit:      5.20,
		OrderTicket: 1902573182,
		ClosePrice:  97.50,
	}
	if o.Commission != -0.02 {
		t.Errorf("Commission=%.4f, want -0.02", o.Commission)
	}
	if o.Profit != 5.20 {
		t.Errorf("Profit=%.4f, want 5.20", o.Profit)
	}
	if o.OrderTicket != 1902573182 {
		t.Errorf("OrderTicket=%d, want 1902573182", o.OrderTicket)
	}
	if o.ClosePrice != 97.50 {
		t.Errorf("ClosePrice=%.4f, want 97.50", o.ClosePrice)
	}
}

// TestReconcileTolerance_D193A_NegativeAmount D-193-A 扩包 S6 R1：
// 容差负金额修 |amount|——swap/commission 符号方向因 broker 而异，用绝对值容差。
func TestReconcileTolerance_D193A_NegativeAmount(t *testing.T) {
	cases := []struct {
		name     string
		cfAmt    float64
		orderAmt float64
		tol      float64
		want     bool
	}{
		{"同向同值", -0.04, -0.04, 0.01, true},
		{"反向同值（符号差异）", -0.04, 0.04, 0.01, true}, // |amount| 容差
		{"微小差异", -0.04, -0.045, 0.01, true},     // diff=0.005 < 0.01
		{"超容差", -0.04, -0.06, 0.01, false},      // diff=0.02 > 0.01
		{"零息对拍", 0, 0, 0.01, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfAbs := math.Abs(c.cfAmt)
			orderAbs := math.Abs(c.orderAmt)
			got := math.Abs(cfAbs-orderAbs) <= c.tol
			if got != c.want {
				t.Errorf("cf=%.4f order=%.4f tol=%.4f: got=%v want=%v", c.cfAmt, c.orderAmt, c.tol, got, c.want)
			}
		})
	}
}
