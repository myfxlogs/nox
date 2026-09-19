// D-274 S1 测试：event_cases + paper_trades Store CRUD + 两预检查询（DSN 真实夹具）。
package pgstore

import (
	"context"
	"testing"
	"time"
)

// eventCasesTestStore 连接 ARB_TEST_PG_DSN，迁移后 TRUNCATE 隔离。
func eventCasesTestStore(t *testing.T) *Store {
	t.Helper()
	s := sharedTestStore(t)
	ctx := context.Background()
	prepareTestSchema(t, s)
	if _, err := s.pool.Exec(ctx, "TRUNCATE event_cases, paper_trades CASCADE"); err != nil {
		t.Fatalf("TRUNCATE: %v", err)
	}
	return s
}

// TestEventCases_InsertAndQuery 插入 event_case → ByAlertID/BySourceURL/LotSizeByCode/OfferPriceByCaseID 全量验证。
func TestEventCases_InsertAndQuery(t *testing.T) {
	s := eventCasesTestStore(t)
	ctx := context.Background()

	announceDate := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	alertID := int64(1001)
	offerPrice := 2.50
	currency := "HKD"
	prePrice := 1.55
	lotSize := 2000
	lotSizeSource := "longbridge"
	premiumPct := 61.29
	score := 7
	suggestedQty := 10
	sourceURL := "https://www.hkexnews.hk/announcement/00626"
	conditions := `["court_approval","shareholder_vote"]`

	c := &EventCase{
		AlertID:       &alertID,
		Code:          "00626.HK",
		Market:        "HK",
		EventType:     "privatisation",
		AnnounceDate:  announceDate,
		OfferPrice:    &offerPrice,
		Currency:      &currency,
		PrePrice:      &prePrice,
		LotSize:       &lotSize,
		LotSizeSource: &lotSizeSource,
		PremiumPct:    &premiumPct,
		Conditions:    &conditions,
		RiskSignal:    stringPtr("green"),
		Score:         &score,
		SuggestedQty:  &suggestedQty,
		Outcome:       "pending",
		SourceURL:     &sourceURL,
	}
	if err := s.InsertEventCase(ctx, c); err != nil {
		t.Fatalf("InsertEventCase: %v", err)
	}
	if c.ID == 0 || c.CreatedAt.IsZero() {
		t.Fatalf("回填失败：id=%d created_at=%v", c.ID, c.CreatedAt)
	}

	// 去重预检①：同 source_url 已建 case
	exists, err := s.EventCaseBySourceURL(ctx, sourceURL)
	if err != nil {
		t.Fatalf("EventCaseBySourceURL: %v", err)
	}
	if !exists {
		t.Fatalf("EventCaseBySourceURL 应返回 true")
	}
	// 不同 URL 应返回 false
	exists2, _ := s.EventCaseBySourceURL(ctx, "https://other.example")
	if exists2 {
		t.Fatalf("EventCaseBySourceURL 不同 URL 应返回 false")
	}

	// 去重预检②：同 alert_id 已建 case
	exists3, err := s.EventCaseByAlertID(ctx, alertID)
	if err != nil {
		t.Fatalf("EventCaseByAlertID: %v", err)
	}
	if !exists3 {
		t.Fatalf("EventCaseByAlertID 应返回 true")
	}

	// LotSizeByCode：同 code 历史 lot_size
	ls, found, err := s.LotSizeByCode(ctx, "00626.HK")
	if err != nil {
		t.Fatalf("LotSizeByCode: %v", err)
	}
	if !found || ls != 2000 {
		t.Fatalf("LotSizeByCode 应返回 2000，got ls=%d found=%v", ls, found)
	}
	// 不同 code 应 not found
	_, found2, _ := s.LotSizeByCode(ctx, "99999.HK")
	if found2 {
		t.Fatalf("LotSizeByCode 不同 code 应 not found")
	}

	// OfferPriceByCaseID
	op, err := s.OfferPriceByCaseID(ctx, int(c.ID))
	if err != nil {
		t.Fatalf("OfferPriceByCaseID: %v", err)
	}
	if op != 2.50 {
		t.Fatalf("OfferPriceByCaseID 应返回 2.50，got %v", op)
	}
}

// TestEventCases_ListAndOutcome ListEventCases + UpdateEventCaseOutcome 验证。
func TestEventCases_ListAndOutcome(t *testing.T) {
	s := eventCasesTestStore(t)
	ctx := context.Background()

	announceDate := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	sourceURL := "https://www.hkexnews.hk/test"
	c := &EventCase{
		Code:         "00700.HK",
		Market:       "HK",
		EventType:    "merger",
		AnnounceDate: announceDate,
		Outcome:      "pending",
		SourceURL:    &sourceURL,
	}
	if err := s.InsertEventCase(ctx, c); err != nil {
		t.Fatalf("InsertEventCase: %v", err)
	}

	// ListEventCases
	cases, err := s.ListEventCases(ctx, 10)
	if err != nil {
		t.Fatalf("ListEventCases: %v", err)
	}
	if len(cases) != 1 || cases[0].Code != "00700.HK" {
		t.Fatalf("ListEventCases 应返回 1 行 00700.HK，got %d 行", len(cases))
	}

	// UpdateEventCaseOutcome
	settleDate := time.Date(2027, 3, 8, 0, 0, 0, 0, time.UTC)
	settlePrice := 2.50
	failDrawdown := 0.295
	if err := s.UpdateEventCaseOutcome(ctx, int(c.ID), "success", &settleDate, &settlePrice, &failDrawdown); err != nil {
		t.Fatalf("UpdateEventCaseOutcome: %v", err)
	}
	updated, _ := s.EventCaseByID(ctx, int(c.ID))
	if updated.Outcome != "success" || updated.SettlePrice == nil || *updated.SettlePrice != 2.50 {
		t.Fatalf("UpdateEventCaseOutcome 后 outcome=%s settle_price=%v", updated.Outcome, updated.SettlePrice)
	}
}

// TestPaperTrades_InsertAndList 插入 paper_trade → ListPaperTrades + ByCaseID 验证。
func TestPaperTrades_InsertAndList(t *testing.T) {
	s := eventCasesTestStore(t)
	ctx := context.Background()

	// 先建 event_case（paper_trade 外键依赖）
	announceDate := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	alertID := int64(2002)
	offerPrice := 2.50
	prePrice := 1.55
	c := &EventCase{
		AlertID:      &alertID,
		Code:         "00626.HK",
		Market:       "HK",
		EventType:    "privatisation",
		AnnounceDate: announceDate,
		OfferPrice:   &offerPrice,
		PrePrice:     &prePrice,
		Outcome:      "pending",
	}
	if err := s.InsertEventCase(ctx, c); err != nil {
		t.Fatalf("InsertEventCase: %v", err)
	}

	// InsertPaperTrade
	suggestedQty := 10
	modelNetReturn := 0.613
	modelFailDrawdown := 0.295
	modelSuccessRate := 0.85
	pt := &PaperTrade{
		CaseID:               int(c.ID),
		AlertID:              &alertID,
		SourceType:           "hkex",
		Code:                 "00626.HK",
		Market:               "HK",
		Direction:            "buy",
		SuggestedQty:         &suggestedQty,
		SuggestedPrice:       &prePrice,
		Status:               "suggested",
		Confidence:           "high",
		ModelNetReturnPct:    &modelNetReturn,
		ModelFailDrawdownPct: &modelFailDrawdown,
		ModelSuccessRate:     &modelSuccessRate,
	}
	if err := s.InsertPaperTrade(ctx, pt); err != nil {
		t.Fatalf("InsertPaperTrade: %v", err)
	}
	if pt.ID == 0 || pt.CreatedAt.IsZero() {
		t.Fatalf("回填失败：id=%d created_at=%v", pt.ID, pt.CreatedAt)
	}

	// PaperTradeByCaseID：已建过 → true
	exists, err := s.PaperTradeByCaseID(ctx, int(c.ID))
	if err != nil {
		t.Fatalf("PaperTradeByCaseID: %v", err)
	}
	if !exists {
		t.Fatalf("PaperTradeByCaseID 应返回 true")
	}

	// ListPaperTrades
	trades, err := s.ListPaperTrades(ctx, "", 10)
	if err != nil {
		t.Fatalf("ListPaperTrades: %v", err)
	}
	if len(trades) != 1 || trades[0].Code != "00626.HK" {
		t.Fatalf("ListPaperTrades 应返回 1 行 00626.HK，got %d 行", len(trades))
	}
	// 按 status 过滤
	tradesSuggested, _ := s.ListPaperTrades(ctx, "suggested", 10)
	if len(tradesSuggested) != 1 {
		t.Fatalf("ListPaperTrades status=suggested 应返回 1 行，got %d", len(tradesSuggested))
	}
	tradesSettled, _ := s.ListPaperTrades(ctx, "settled", 10)
	if len(tradesSettled) != 0 {
		t.Fatalf("ListPaperTrades status=settled 应返回 0 行，got %d", len(tradesSettled))
	}
}

// TestPaperTrades_Settle SettlePaperTrade + PaperTradeByID 验证（M3 §2 结算公式）。
func TestPaperTrades_Settle(t *testing.T) {
	s := eventCasesTestStore(t)
	ctx := context.Background()

	// 建 event_case + paper_trade
	announceDate := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	offerPrice := 2.50
	prePrice := 1.55
	c := &EventCase{
		Code:         "00626.HK",
		Market:       "HK",
		EventType:    "privatisation",
		AnnounceDate: announceDate,
		OfferPrice:   &offerPrice,
		PrePrice:     &prePrice,
		Outcome:      "pending",
	}
	if err := s.InsertEventCase(ctx, c); err != nil {
		t.Fatalf("InsertEventCase: %v", err)
	}
	suggestedQty := 10
	pt := &PaperTrade{
		CaseID:         int(c.ID),
		SourceType:     "hkex",
		Code:           "00626.HK",
		Market:         "HK",
		Direction:      "buy",
		SuggestedQty:   &suggestedQty,
		SuggestedPrice: &prePrice,
		Status:         "suggested",
		Confidence:     "high",
	}
	if err := s.InsertPaperTrade(ctx, pt); err != nil {
		t.Fatalf("InsertPaperTrade: %v", err)
	}

	// SettlePaperTrade：success 未填 actual_price → actualNetReturnPct 用 offer_price 算（M3 §2 v4.5 S1）
	// success: sellPrice=actualPrice(0)→用 offerPrice=2.50; actualNetReturnPct=(2.50-1.55)/1.55=0.6129...
	expectedNetReturn := (2.50 - 1.55) / 1.55
	actualNetReturn := expectedNetReturn
	if err := s.SettlePaperTrade(ctx, pt.ID, "success", nil, &actualNetReturn); err != nil {
		t.Fatalf("SettlePaperTrade: %v", err)
	}

	// PaperTradeByID 验证
	got, err := s.PaperTradeByID(ctx, pt.ID)
	if err != nil {
		t.Fatalf("PaperTradeByID: %v", err)
	}
	if got.Status != "settled" {
		t.Fatalf("结算后 status 应=settled，got %s", got.Status)
	}
	if got.ActualOutcome == nil || *got.ActualOutcome != "success" {
		t.Fatalf("actual_outcome 应=success，got %v", got.ActualOutcome)
	}
	if got.SettledAt == nil {
		t.Fatalf("settled_at 应非 nil")
	}
	if got.ActualNetReturnPct == nil || *got.ActualNetReturnPct < 0.61 || *got.ActualNetReturnPct > 0.62 {
		t.Fatalf("actual_net_return_pct 应≈0.6129，got %v", got.ActualNetReturnPct)
	}
}

// stringPtr 辅助：返回字符串指针。
func stringPtr(s string) *string { return &s }
