// Package dashboard 实现 DashboardService RPC handler（§8.2）。
package dashboard

import (
	"context"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/fact"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// Service 实现 DashboardServiceHandler 接口。
type Service struct {
	store    *pgstore.Store
	qbus     *bus.QuoteBus
	pipeline *exec.Pipeline
	bm       *BrokerManager
	// reviewer 复盘触发（§13.6，D-034）。nil = 未接线（RPC 仍可用，仅触发不跑）。
	reviewer func()
}

// New 创建 dashboard 服务。reviewer 为复盘触发闭包（nil 则平仓后不触发）。
func New(store *pgstore.Store, qbus *bus.QuoteBus, pipeline *exec.Pipeline, bm *BrokerManager, reviewer func()) *Service {
	return &Service{store: store, qbus: qbus, pipeline: pipeline, bm: bm, reviewer: reviewer}
}

// --- 快照类 ---

func (s *Service) ListLatestQuotes(ctx context.Context, req *connect.Request[v1.ListQuotesRequest]) (*connect.Response[v1.ListQuotesResponse], error) {
	symbol := req.Msg.GetSymbol()
	latest := s.qbus.AllLatest() // 全量（Snapshot(nil) 会返回空——Snap 按 symbols 过滤，nil = 空集）
	var quotes []*v1.Quote
	for _, q := range latest {
		if symbol != "" && q.Symbol != symbol {
			continue
		}
		quotes = append(quotes, quoteToProto(q))
	}
	return connect.NewResponse(&v1.ListQuotesResponse{Quotes: quotes}), nil
}

func quoteToProto(q bus.Quote) *v1.Quote {
	return &v1.Quote{
		Symbol:      q.Symbol,
		Venue:       q.Venue,
		Bid:         q.Bid,
		Ask:         q.Ask,
		Time:        timestamppb.New(q.Time),
		VenueSymbol: q.VenueSymbol,
		VenueType:   q.VenueType,
	}
}

func (s *Service) ListLatestFacts(ctx context.Context, req *connect.Request[v1.ListLatestFactsRequest]) (*connect.Response[v1.ListLatestFactsResponse], error) {
	facts, err := s.store.LatestFacts(ctx, req.Msg.GetKind(), req.Msg.GetVenue(), req.Msg.GetSymbol())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pbFacts []*v1.Fact
	for _, f := range facts {
		pbFacts = append(pbFacts, factToProto(&f))
	}
	return connect.NewResponse(&v1.ListLatestFactsResponse{Facts: pbFacts}), nil
}

func (s *Service) ListOpportunities(ctx context.Context, req *connect.Request[v1.ListOpportunitiesRequest]) (*connect.Response[v1.ListOpportunitiesResponse], error) {
	limit := int(req.Msg.GetLimit())
	offset := int(req.Msg.GetOffset())
	opps, err := s.store.ListOpportunities(ctx, req.Msg.GetType(), limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pbOpps []*v1.Opportunity
	for _, o := range opps {
		pbOpps = append(pbOpps, oppToProto(&o))
	}
	return connect.NewResponse(&v1.ListOpportunitiesResponse{Opportunities: pbOpps}), nil
}

func (s *Service) ListAlerts(ctx context.Context, req *connect.Request[v1.ListAlertsRequest]) (*connect.Response[v1.ListAlertsResponse], error) {
	limit := int(req.Msg.GetLimit())
	offset := int(req.Msg.GetOffset())
	if limit <= 0 {
		limit = 100
	}
	alerts, err := s.store.ListAlerts(ctx, limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pbAlerts []*v1.Alert
	for _, a := range alerts {
		pbAlerts = append(pbAlerts, alertToProto(&a))
	}
	return connect.NewResponse(&v1.ListAlertsResponse{Alerts: pbAlerts}), nil
}

func (s *Service) ListTriggerStates(ctx context.Context, req *connect.Request[v1.ListTriggerStatesRequest]) (*connect.Response[v1.ListTriggerStatesResponse], error) {
	return connect.NewResponse(&v1.ListTriggerStatesResponse{}), nil
}

func (s *Service) ListSourceHealth(ctx context.Context, req *connect.Request[v1.ListSourceHealthRequest]) (*connect.Response[v1.ListSourceHealthResponse], error) {
	return connect.NewResponse(&v1.ListSourceHealthResponse{}), nil
}

func (s *Service) ListFacts(ctx context.Context, req *connect.Request[v1.ListFactsRequest]) (*connect.Response[v1.ListFactsResponse], error) {
	limit := int(req.Msg.GetLimit())
	offset := int(req.Msg.GetOffset())
	if limit <= 0 {
		limit = 100
	}
	facts, err := s.store.QueryFacts(ctx, req.Msg.GetKind(), req.Msg.GetVenue(), req.Msg.GetSymbol(), limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pbFacts []*v1.Fact
	for _, f := range facts {
		pbFacts = append(pbFacts, factToProto(&f))
	}
	return connect.NewResponse(&v1.ListFactsResponse{Facts: pbFacts}), nil
}

// --- 告警确认 ---

func (s *Service) AckAlert(ctx context.Context, req *connect.Request[v1.AckAlertRequest]) (*connect.Response[v1.AckAlertResponse], error) {
	if err := s.store.AckAlert(ctx, req.Msg.GetId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.AckAlertResponse{}), nil
}

func (s *Service) AckAll(ctx context.Context, req *connect.Request[v1.AckAllRequest]) (*connect.Response[v1.AckAllResponse], error) {
	if err := s.store.AckAll(ctx); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.AckAllResponse{}), nil
}

func (s *Service) ListUnacked(ctx context.Context, req *connect.Request[v1.ListUnackedRequest]) (*connect.Response[v1.ListUnackedResponse], error) {
	alerts, _, err := s.store.ListUnacked(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pbAlerts []*v1.Alert
	for _, a := range alerts {
		pbAlerts = append(pbAlerts, alertToProto(&a))
	}
	return connect.NewResponse(&v1.ListUnackedResponse{Alerts: pbAlerts}), nil
}

// --- 台账（§2.8） ---

func (s *Service) AddLedgerEntry(ctx context.Context, req *connect.Request[v1.AddLedgerEntryRequest]) (*connect.Response[v1.AddLedgerEntryResponse], error) {
	m := req.Msg
	date := time.Now()
	if m.Date != nil {
		date = m.Date.AsTime()
	}
	id, err := s.store.AddLedgerEntry(ctx, &pgstore.LedgerEntry{
		Date:     date,
		Channel:  m.Channel,
		Currency: m.Currency,
		Amount:   m.Amount,
		FeeRate:  m.FeeRate,
		Tier:     m.Tier,
		Note:     m.Note,
	})
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.AddLedgerEntryResponse{Id: id}), nil
}

func (s *Service) ListLedgerEntries(ctx context.Context, req *connect.Request[v1.ListLedgerEntriesRequest]) (*connect.Response[v1.ListLedgerEntriesResponse], error) {
	entries, err := s.store.ListLedgerEntries(ctx, int(req.Msg.GetLimit()), int(req.Msg.GetOffset()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pb []*v1.LedgerEntry
	for i := range entries {
		e := &entries[i]
		pb = append(pb, &v1.LedgerEntry{
			Id:       e.ID,
			Date:     timestamppb.New(e.Date),
			Channel:  e.Channel,
			Currency: e.Currency,
			Amount:   e.Amount,
			FeeRate:  e.FeeRate,
			Tier:     e.Tier,
			Note:     e.Note,
		})
	}
	return connect.NewResponse(&v1.ListLedgerEntriesResponse{Entries: pb}), nil
}

func (s *Service) LedgerSummary(ctx context.Context, req *connect.Request[v1.LedgerSummaryRequest]) (*connect.Response[v1.LedgerSummaryResponse], error) {
	summaries, err := s.store.LedgerSummary(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pb []*v1.TierSummary
	for i := range summaries {
		t := &summaries[i]
		pb = append(pb, &v1.TierSummary{
			Tier:       t.Tier,
			Inflow:     t.Inflow,
			Outflow:    t.Outflow,
			Net:        t.Net,
			EntryCount: int32(t.EntryCount),
		})
	}
	return connect.NewResponse(&v1.LedgerSummaryResponse{Items: pb}), nil
}

// --- 经验库（§2.9/§13.6） ---

func (s *Service) ListKnowledgeEntries(ctx context.Context, req *connect.Request[v1.ListKnowledgeEntriesRequest]) (*connect.Response[v1.ListKnowledgeEntriesResponse], error) {
	entries, err := s.store.ListKnowledgeEntries(ctx, "", 200, 0)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pb []*v1.KnowledgeEntry
	for i := range entries {
		e := &entries[i]
		pe := &v1.KnowledgeEntry{
			Id:             e.ID,
			Signature:      e.Signature,
			Venue:          e.Venue,
			Symbol:         e.Symbol,
			Verdict:        e.Verdict,
			Rationale:      e.Rationale,
			Source:         e.Source,
			Status:         e.Status,
			ValidationNote: e.ValidationNote,
		}
		if !e.Ts.IsZero() {
			pe.Ts = timestamppb.New(e.Ts)
		}
		if e.ValidatedAt != nil {
			pe.ValidatedAt = timestamppb.New(*e.ValidatedAt)
		}
		pb = append(pb, pe)
	}
	return connect.NewResponse(&v1.ListKnowledgeEntriesResponse{Entries: pb}), nil
}

func (s *Service) ReviewKnowledgeEntry(ctx context.Context, req *connect.Request[v1.ReviewKnowledgeEntryRequest]) (*connect.Response[v1.ReviewKnowledgeEntryResponse], error) {
	m := req.Msg
	if err := s.store.UpdateKnowledgeEntryReview(ctx, m.Signature, m.Status, m.Verdict, m.ValidationNote); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	return connect.NewResponse(&v1.ReviewKnowledgeEntryResponse{}), nil
}

// --- 执行（§13） ---

func (s *Service) ExecuteOpportunity(ctx context.Context, req *connect.Request[v1.ExecuteOpportunityRequest]) (*connect.Response[v1.ExecuteOpportunityResponse], error) {
	if s.pipeline == nil {
		return connect.NewResponse(&v1.ExecuteOpportunityResponse{
			Status:  "FAILED",
			Message: "executor not wired",
		}), nil
	}
	// 查机会记录获取 leg 规格
	oppID := req.Msg.GetOpportunityId()
	opps, err := s.store.ListOpportunities(ctx, "", 200, 0)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var found *pgstore.Opportunity
	for i := range opps {
		if opps[i].ID == oppID {
			found = &opps[i]
			break
		}
	}
	if found == nil {
		return connect.NewResponse(&v1.ExecuteOpportunityResponse{
			Status:  "FAILED",
			Message: "opportunity not found or expired",
		}), nil
	}
	// 复核：报价新鲜度 + 净收益劣化
	if !s.pipeline.Revalidate(found.VenueBuy, found.VenueBuy, found.VenueSell, found.NetBps) {
		return connect.NewResponse(&v1.ExecuteOpportunityResponse{
			Status:  "FAILED",
			Message: "quote stale or net benefit degraded beyond threshold",
		}), nil
	}
	// 构造双腿（简化：qty 从请求传入，卖腿服务端反算）
	qty := req.Msg.GetQty()
	legs := []exec.LegSpec{
		{VenueKey: found.VenueBuy, VenueSymbol: found.VenueBuy, Side: "BUY", Qty: qty},
		{VenueKey: found.VenueSell, VenueSymbol: found.VenueSell, Side: "SELL", Qty: qty},
	}
	result, err := s.pipeline.Execute(ctx, oppID, req.Msg.GetRequestId(), legs)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.ExecuteOpportunityResponse{
		PositionId: result.PositionID,
		Status:     result.Status,
	}), nil
}

func (s *Service) ClosePosition(ctx context.Context, req *connect.Request[v1.ClosePositionRequest]) (*connect.Response[v1.ClosePositionResponse], error) {
	if s.pipeline == nil {
		return connect.NewResponse(&v1.ClosePositionResponse{
			Status:  "FAILED",
			Message: "executor not wired",
		}), nil
	}
	if err := s.pipeline.ClosePosition(ctx, req.Msg.GetPositionId()); err != nil {
		return connect.NewResponse(&v1.ClosePositionResponse{
			Status:  "FAILED",
			Message: err.Error(),
		}), nil
	}
	// 平仓成功 → 触发统计复盘（§13.6，D-034；fire-and-forget，不阻塞 RPC）
	if s.reviewer != nil {
		go s.reviewer()
	}
	return connect.NewResponse(&v1.ClosePositionResponse{
		Status: "CLOSED",
	}), nil
}

func (s *Service) ListPositions(ctx context.Context, req *connect.Request[v1.ListPositionsRequest]) (*connect.Response[v1.ListPositionsResponse], error) {
	limit := int(req.Msg.GetLimit())
	offset := int(req.Msg.GetOffset())
	if limit <= 0 {
		limit = 50
	}
	positions, err := s.store.ListPositions(ctx, req.Msg.GetStatus(), limit, offset)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pbPositions []*v1.Position
	for _, p := range positions {
		pbPositions = append(pbPositions, positionToProto(&p))
	}
	return connect.NewResponse(&v1.ListPositionsResponse{Positions: pbPositions}), nil
}

// --- 实时推送（§8.2 D-026） ---

func (s *Service) StreamUpdates(ctx context.Context, req *connect.Request[v1.StreamUpdatesRequest], stream *connect.ServerStream[v1.StreamUpdatesResponse]) error {
	// 实时推送报价：订阅 QuoteBus 全量（wildcard ""），亚秒级行情走推流。
	// 机会/告警/持仓由引擎 30s 节流产生、无独立事件总线 → 前端以 30s 低频快照补齐。
	ch, cancel := s.qbus.Subscribe("")
	defer cancel()
	// keepalive 每 30s（§8.2 D-027）
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case q, ok := <-ch:
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_Quote{
					Quote: quoteToProto(q),
				},
			}); err != nil {
				return err
			}
		case <-ticker.C:
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_Keepalive{
					Keepalive: &v1.Keepalive{},
				},
			}); err != nil {
				return err
			}
		}
	}
}

// --- 健康 ---

func (s *Service) Health(ctx context.Context, req *connect.Request[v1.HealthRequest]) (*connect.Response[v1.HealthResponse], error) {
	return connect.NewResponse(&v1.HealthResponse{
		Ok:      true,
		DbOk:    s.store != nil,
		Version: "0.1.0",
	}), nil
}

// --- 转换辅助 ---

func factToProto(f *fact.Fact) *v1.Fact {
	return &v1.Fact{
		Kind:   f.Kind,
		Venue:  f.Venue,
		Symbol: f.Symbol,
		Value:  f.Value,
		Unit:   f.Unit,
		Ts:     timestamppb.New(f.Ts),
		Src:    f.Src,
	}
}

func oppToProto(o *pgstore.Opportunity) *v1.Opportunity {
	pb := &v1.Opportunity{
		Id:             o.ID,
		Type:           o.Type,
		VenueBuy:       o.VenueBuy,
		VenueSell:      o.VenueSell,
		PriceBuy:       o.PriceBuy,
		PriceSell:      o.PriceSell,
		GrossBps:       o.GrossBps,
		SpreadCost:     o.SpreadCost,
		CommissionCost: o.CommissionCost,
		SlippageCost:   o.SlippageCost,
		SwapCost:       o.SwapCost,
		NetBps:         o.NetBps,
	}
	if o.BreakevenDays != nil {
		pb.BreakevenDays = o.BreakevenDays
	}
	if !o.QuoteTime.IsZero() {
		pb.QuoteTime = timestamppb.New(o.QuoteTime)
	}
	if !o.ExpiresAt.IsZero() {
		pb.ExpiresAt = timestamppb.New(o.ExpiresAt)
	}
	if !o.CreatedAt.IsZero() {
		pb.CreatedAt = timestamppb.New(o.CreatedAt)
	}
	if !o.UpdatedAt.IsZero() {
		pb.UpdatedAt = timestamppb.New(o.UpdatedAt)
	}
	return pb
}

func alertToProto(a *pgstore.Alert) *v1.Alert {
	pb := &v1.Alert{
		Id:       a.ID,
		RuleName: a.RuleName,
		Level:    a.Level,
		Message:  a.Message,
		Acked:    a.Acked,
	}
	if a.RuleID != nil {
		pb.RuleId = a.RuleID
	}
	if !a.Ts.IsZero() {
		pb.Ts = timestamppb.New(a.Ts)
	}
	return pb
}

func positionToProto(p *pgstore.Position) *v1.Position {
	pb := &v1.Position{
		Id:             p.ID,
		OpportunityId:  p.OpportunityID,
		Type:           p.Type,
		Status:         p.Status,
		ExpectedNetBps: p.ExpectedNetBps,
		Note:           p.Note,
	}
	if p.RealizedPnl != nil {
		pb.RealizedPnl = p.RealizedPnl
	}
	if p.OpenedAt != nil {
		pb.OpenedAt = timestamppb.New(*p.OpenedAt)
	}
	if p.ClosedAt != nil {
		pb.ClosedAt = timestamppb.New(*p.ClosedAt)
	}
	return pb
}
