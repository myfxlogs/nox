// D-274 S8 事件驱动研判引擎 RPC handler（M3 §4 + M4 §1）。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/judge"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// ListEventJudgments 列出 event_cases（created_at DESC，M3 §4）。
func (s *Service) ListEventJudgments(ctx context.Context, req *connect.Request[v1.ListEventJudgmentsRequest]) (*connect.Response[v1.ListEventJudgmentsResponse], error) {
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 100
	}
	cases, err := s.store.ListEventCases(ctx, limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list event_cases: %w", err))
	}
	rows := make([]*v1.EventJudgmentRow, 0, len(cases))
	for i := range cases {
		rows = append(rows, eventCaseToProto(&cases[i]))
	}
	return connect.NewResponse(&v1.ListEventJudgmentsResponse{Rows: rows}), nil
}

// ListPaperTrades 列出 paper_trades（M3 §4）。
func (s *Service) ListPaperTrades(ctx context.Context, req *connect.Request[v1.ListPaperTradesRequest]) (*connect.Response[v1.ListPaperTradesResponse], error) {
	limit := int(req.Msg.GetLimit())
	if limit <= 0 {
		limit = 100
	}
	trades, err := s.store.ListPaperTrades(ctx, req.Msg.GetStatus(), limit)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list paper_trades: %w", err))
	}
	rows := make([]*v1.PaperTradeRow, 0, len(trades))
	for i := range trades {
		rows = append(rows, paperTradeToProto(&trades[i]))
	}
	return connect.NewResponse(&v1.ListPaperTradesResponse{Rows: rows}), nil
}

// AcceptJudgment HITL 门：业主接受研判 → 建 paper_trade（M3 §4）。
// 四步校验：①存在 ②pending ③未建过 paper_trade ④INSERT suggested_qty 直读不重算。
func (s *Service) AcceptJudgment(ctx context.Context, req *connect.Request[v1.AcceptJudgmentRequest]) (*connect.Response[v1.AcceptJudgmentResponse], error) {
	caseID := int(req.Msg.GetEventCaseId())
	if caseID <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("event_case_id 必须为正"))
	}

	// ① 校验 event_case 存在
	ec, err := s.store.EventCaseByID(ctx, caseID)
	if err != nil {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("event_case %d 不存在: %w", caseID, err))
	}

	// ② 校验 outcome=pending
	if ec.Outcome != "pending" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("event_case %d outcome=%q 非 pending", caseID, ec.Outcome))
	}

	// ③ 校验未建过 paper_trade（case_id UNIQUE）
	exists, err := s.store.PaperTradeByCaseID(ctx, caseID)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("check paper_trade by case_id: %w", err))
	}
	if exists {
		return nil, connect.NewError(connect.CodeAlreadyExists, fmt.Errorf("event_case %d 已建过 paper_trade", caseID))
	}

	// ④ INSERT paper_trades（suggested_qty 直读不重算，suggested_price=pre_price）
	pt := &pgstore.PaperTrade{
		CaseID:         caseID,
		AlertID:        ec.AlertID,
		SourceType:     sourceTypeFromURL(ec.SourceURL),
		Code:           ec.Code,
		Market:         ec.Market,
		Direction:      "buy",
		SuggestedQty:   ec.SuggestedQty,
		SuggestedPrice: ec.PrePrice,
		Status:         "suggested",
		Confidence:     judge.ConfidenceLevel(0), // 置信度由研判时存，此处占位（v2 从 event_cases 读）
	}
	if err := s.store.InsertPaperTrade(ctx, pt); err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("insert paper_trade: %w", err))
	}

	log.Printf("[d274] AcceptJudgment: case_id=%d → paper_trade_id=%d", caseID, pt.ID)
	s.PublishDataRefresh(RefreshEvents) // D-007 S3：paper_trades 写毕→EventsPage 失效触发
	return connect.NewResponse(&v1.AcceptJudgmentResponse{PaperTradeId: pt.ID}), nil
}

// SettlePaperTrade 结算纸面交易（M3 §2 + M3 §4）。
func (s *Service) SettlePaperTrade(ctx context.Context, req *connect.Request[v1.SettlePaperTradeRequest]) (*connect.Response[v1.SettlePaperTradeResponse], error) {
	tradeID := req.Msg.GetPaperTradeId()
	outcome := req.Msg.GetActualOutcome()
	actualPrice := req.Msg.GetActualPrice()

	if tradeID <= 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("paper_trade_id 必须为正"))
	}
	if outcome != "success" && outcome != "failed" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("actual_outcome 必须为 success/failed，got %q", outcome))
	}

	result, err := judge.SettlePaperTrade(ctx, s.store, tradeID, outcome, actualPrice)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("settle paper_trade: %w", err))
	}

	resp := &v1.SettlePaperTradeResponse{Ok: true}
	if result.ActualNetReturnPct != nil {
		resp.ActualNetReturnPct = result.ActualNetReturnPct
	}
	s.PublishDataRefresh(RefreshEvents) // D-007 S3
	return connect.NewResponse(resp), nil
}

// eventCaseToProto EventCase → proto EventJudgmentRow。
func eventCaseToProto(ec *pgstore.EventCase) *v1.EventJudgmentRow {
	row := &v1.EventJudgmentRow{
		Id:           ec.ID,
		Code:         ec.Code,
		Market:       ec.Market,
		EventType:    ec.EventType,
		Outcome:      ec.Outcome,
		AnnounceDate: ec.AnnounceDate.Format("2006-01-02"),
		CreatedAt:    ec.CreatedAt.Format(time.RFC3339),
	}
	if ec.AlertID != nil {
		row.AlertId = ec.AlertID
	}
	if ec.OfferPrice != nil {
		row.OfferPrice = ec.OfferPrice
	}
	if ec.Currency != nil {
		row.Currency = ec.Currency
	}
	if ec.PrePrice != nil {
		row.PrePrice = ec.PrePrice
	}
	if ec.LotSize != nil {
		row.LotSize = pInt32(int32(*ec.LotSize))
	}
	if ec.LotSizeSource != nil {
		row.LotSizeSource = ec.LotSizeSource
	}
	if ec.PremiumPct != nil {
		row.PremiumPct = ec.PremiumPct
	}
	if ec.OfferType != nil {
		row.OfferType = ec.OfferType
	}
	if ec.Proration != nil {
		row.Proration = ec.Proration
	}
	if ec.Conditions != nil {
		row.Conditions = ec.Conditions
	}
	if ec.AcquirerStrength != nil {
		row.AcquirerStrength = ec.AcquirerStrength
	}
	if ec.RiskSignal != nil {
		row.RiskSignal = ec.RiskSignal
	}
	if ec.Score != nil {
		row.Score = pInt32(int32(*ec.Score))
	}
	if ec.SuggestedQty != nil {
		row.SuggestedQty = pInt32(int32(*ec.SuggestedQty))
	}
	if ec.SettleDate != nil {
		row.SettleDate = pStr(ec.SettleDate.Format("2006-01-02"))
	}
	if ec.SettlePrice != nil {
		row.SettlePrice = ec.SettlePrice
	}
	if ec.FailDrawdownPct != nil {
		row.FailDrawdownPct = ec.FailDrawdownPct
	}
	if ec.HoldingDays != nil {
		row.HoldingDays = pInt32(int32(*ec.HoldingDays))
	}
	if ec.SourceURL != nil {
		row.SourceUrl = ec.SourceURL
	}
	return row
}

// paperTradeToProto PaperTrade → proto PaperTradeRow。
func paperTradeToProto(pt *pgstore.PaperTrade) *v1.PaperTradeRow {
	row := &v1.PaperTradeRow{
		Id:         pt.ID,
		CaseId:     int32(pt.CaseID),
		SourceType: pt.SourceType,
		Code:       pt.Code,
		Market:     pt.Market,
		Direction:  pt.Direction,
		Status:     pt.Status,
		Confidence: pt.Confidence,
		CreatedAt:  pt.CreatedAt.Format(time.RFC3339),
	}
	if pt.AlertID != nil {
		row.AlertId = pt.AlertID
	}
	if pt.SuggestedQty != nil {
		row.SuggestedQty = pInt32(int32(*pt.SuggestedQty))
	}
	if pt.SuggestedPrice != nil {
		row.SuggestedPrice = pt.SuggestedPrice
	}
	if pt.ActualPrice != nil {
		row.ActualPrice = pt.ActualPrice
	}
	if pt.SampleSize != nil {
		row.SampleSize = pInt32(int32(*pt.SampleSize))
	}
	if pt.Rationale != nil {
		row.Rationale = pt.Rationale
	}
	if pt.ModelNetReturnPct != nil {
		row.ModelNetReturnPct = pt.ModelNetReturnPct
	}
	if pt.ModelFailDrawdownPct != nil {
		row.ModelFailDrawdownPct = pt.ModelFailDrawdownPct
	}
	if pt.ModelSuccessRate != nil {
		row.ModelSuccessRate = pt.ModelSuccessRate
	}
	if pt.HoldingPeriodDays != nil {
		row.HoldingPeriodDays = pInt32(int32(*pt.HoldingPeriodDays))
	}
	if pt.ActualNetReturnPct != nil {
		row.ActualNetReturnPct = pt.ActualNetReturnPct
	}
	if pt.ActualOutcome != nil {
		row.ActualOutcome = pt.ActualOutcome
	}
	if pt.SettledAt != nil {
		row.SettledAt = pStr(pt.SettledAt.Format(time.RFC3339))
	}
	return row
}

// pInt32 辅助构造 *int32。
func pInt32(v int32) *int32 { return &v }
func pStr(v string) *string { return &v }

// sourceTypeFromURL 从 source_url 推断 source_type（hkex/edgar，M2 §2）。
func sourceTypeFromURL(url *string) string {
	if url == nil {
		return "unknown"
	}
	u := *url
	if contains(u, "HKEXnews") || contains(u, "hkexnews") {
		return "hkex"
	}
	if contains(u, "EDGAR") || contains(u, "edgar") || contains(u, "sec.gov") {
		return "edgar"
	}
	return "unknown"
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
