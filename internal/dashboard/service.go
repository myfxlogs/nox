// Package dashboard 实现 DashboardService RPC handler（§8.2）。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/fact"
	"arb/internal/position"
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
	// freshTTL 报价陈旧 TTL（复用 config.DetectorFreshnessTTL，G3）——个人持仓快照展示路径拒绝陈旧帧。
	freshTTL time.Duration
	// liqMMR 理论强平维持保证金率（config.ARB_LIQ_MMR，缺省 0.005）——个人持仓强平价理论值计算用。
	liqMMR float64
	// fundingDropRatio 资金费急降预警比例（config.ARB_FUNDING_DROP_RATIO，缺省 0.5）——§11 翻负/急降预警用。
	fundingDropRatio float64
}

// New 创建 dashboard 服务。reviewer 为复盘触发闭包（nil 则平仓后不触发）；
// freshTTL 为报价陈旧阈值（个人持仓展示用，传 config.DetectorFreshnessTTL）；
// liqMMR 为个人持仓理论强平维持保证金率（传 config.LiqMMR）；
// fundingDropRatio 为资金费急降预警比例（传 config.FundingDropRatio）。
func New(store *pgstore.Store, qbus *bus.QuoteBus, pipeline *exec.Pipeline, bm *BrokerManager, reviewer func(), freshTTL time.Duration, liqMMR float64, fundingDropRatio float64) *Service {
	return &Service{store: store, qbus: qbus, pipeline: pipeline, bm: bm, reviewer: reviewer, freshTTL: freshTTL, liqMMR: liqMMR, fundingDropRatio: fundingDropRatio}
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

// --- 记账（财务线工作项 2：结构化投资记录，决定/教训/观察） ---

// AddInvestmentRecord 新增一条投资记录。kind 非法 / content 空 → 拒绝（后端校验，D-043 前端只传值）。
func (s *Service) AddInvestmentRecord(ctx context.Context, req *connect.Request[v1.AddInvestmentRecordRequest]) (*connect.Response[v1.AddInvestmentRecordResponse], error) {
	m := req.Msg
	kind := m.GetKind()
	if !validRecordKind(kind) {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid kind %q: must be decision/lesson/observation", kind))
	}
	if strings.TrimSpace(m.GetContent()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("content required"))
	}
	id, err := s.store.AddInvestmentRecord(ctx, kind, m.GetAsset(), m.GetContent())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.AddInvestmentRecordResponse{Id: id}), nil
}

// ListInvestmentRecords 按 kind/asset/内容子串筛选返回（D-043：筛选/搜索全部后端算）。
func (s *Service) ListInvestmentRecords(ctx context.Context, req *connect.Request[v1.ListInvestmentRecordsRequest]) (*connect.Response[v1.ListInvestmentRecordsResponse], error) {
	m := req.Msg
	records, err := s.store.ListInvestmentRecords(ctx, m.GetKind(), m.GetAsset(), m.GetQ(), int(m.GetLimit()), int(m.GetOffset()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pb []*v1.InvestmentRecord
	for i := range records {
		r := &records[i]
		pb = append(pb, &v1.InvestmentRecord{
			Id:      r.ID,
			Kind:    r.Kind,
			Asset:   r.Asset,
			Content: r.Content,
			Ts:      timestamppb.New(r.Ts),
		})
	}
	return connect.NewResponse(&v1.ListInvestmentRecordsResponse{Items: pb}), nil
}

// validRecordKind 判断 kind 是否合法三态之一。
func validRecordKind(kind string) bool {
	switch kind {
	case "decision", "lesson", "observation":
		return true
	}
	return false
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

// --- 个人持仓（财务线 TRX 持仓接入，D-046 + 实账化工作项 3） ---

// ListPersonalPositions 返回 OPEN 个人持仓 + 后端计算当前价/告警态/实账（D-043 零信任：前端只渲染）。
func (s *Service) ListPersonalPositions(ctx context.Context, req *connect.Request[v1.ListPersonalPositionsRequest]) (*connect.Response[v1.ListPersonalPositionsResponse], error) {
	positions, err := s.store.ListPersonalPositions(ctx, "OPEN", 200, 0)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// 累计利息全量一次 SUM（当前组合仅空单一处利息源，设计 §4）。
	accrued, err := s.store.SumDailyIncomeInterest(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var symbols []string
	for _, p := range positions {
		symbols = append(symbols, p.PriceSymbol)
	}
	snap := s.qbus.Snapshot(ctx, symbols)
	now := time.Now()
	items := make([]*v1.PersonalPosition, 0, len(positions))
	for i := range positions {
		items = append(items, personalPositionToProto(&positions[i], snap[positions[i].PriceSymbol], s.freshTTL, now, accrued, s.liqMMR))
	}
	return connect.NewResponse(&v1.ListPersonalPositionsResponse{Items: items}), nil
}

// personalPositionToProto 组装单个个人持仓响应：取该 symbol 最新（Time 最新）且未超陈旧 TTL 的一帧 mid 为当前价，
// 复用 position.PersonalLevel 算告警态。无报价或全陈旧 → price 保持 0、level 空（诚实空态，G3）。
// 实账计算（D-043，全后端）：pnl/liq_price/距强平%/累计利息。
func personalPositionToProto(p *pgstore.PersonalPosition, venues map[string]bus.Quote, freshTTL time.Duration, now time.Time, accruedInterest float64, liqMMR float64) *v1.PersonalPosition {
	pb := &v1.PersonalPosition{
		Id:                 p.ID,
		Asset:              p.Asset,
		Kind:               p.Kind,
		PriceSymbol:        p.PriceSymbol,
		Note:               p.Note,
		Status:             p.Status,
		AccruedInterestUsd: accruedInterest,
	}
	if p.WarnHigh != nil {
		pb.WarnHigh = *p.WarnHigh
	}
	if p.CritHigh != nil {
		pb.CritHigh = *p.CritHigh
	}
	if p.Qty != nil {
		pb.Qty = *p.Qty
	}
	if p.EntryPrice != nil {
		pb.EntryPrice = *p.EntryPrice
	}
	if p.Leverage != nil {
		pb.Leverage = int32(*p.Leverage)
	}
	if p.OpenedAt != nil {
		pb.OpenedAt = timestamppb.New(*p.OpenedAt)
	}

	// 现价：最新未陈旧帧 mid（无报价/全陈旧 → price 0，实账按无值处理）。
	price := 0.0
	hasPrice := false
	if q, ok := latestQuote(venues, freshTTL, now); ok {
		price = q.Mid()
		hasPrice = true
		pb.Price = price
		pb.Level = position.PersonalLevel(price, p.WarnHigh, p.CritHigh)
	}

	// 浮动盈亏：仅空单腿且 entry 非空，且现价可用（无价不算，防 (entry−0)×qty 假盈亏）。
	if hasPrice && p.Kind == "short_hedge" && p.EntryPrice != nil && p.Qty != nil {
		pb.PnlUsd = (*p.EntryPrice - price) * *p.Qty
	}
	// 强平价：crit_high（手动权威值，非 0）> 理论 = entry × (1 + 1/leverage − MMR)。
	liq := 0.0
	if p.CritHigh != nil && *p.CritHigh != 0 {
		liq = *p.CritHigh
	} else if p.EntryPrice != nil && p.Leverage != nil && *p.Leverage > 0 {
		liq = *p.EntryPrice * (1 + 1/float64(*p.Leverage) - liqMMR)
	}
	pb.LiqPrice = liq
	// 距强平 % = (liq − 现价) / 现价；负数 = 已破线。liq 或现价不可用 → 0。
	if liq != 0 && hasPrice && price != 0 {
		pb.DistToLiqPct = (liq - price) / price
	}
	return pb
}

// latestQuote 返回 venues 中 Time 最新且未超陈旧 TTL 的一帧（G3，拒绝陈旧帧冒充现价）。
func latestQuote(venues map[string]bus.Quote, freshTTL time.Duration, now time.Time) (bus.Quote, bool) {
	var best bus.Quote
	found := false
	for _, q := range venues {
		if now.Sub(q.Time) > freshTTL {
			continue // 陈旧帧丢弃
		}
		if !found || q.Time.After(best.Time) {
			best = q
			found = true
		}
	}
	return best, found
}

// --- 每日收益（财务线工作项 3：个人持仓实账化） ---

// AddDailyIncome 录入一条每日收益记录。前端只传 day/energy/voting/interest/note；
// price/pnl_usd 后端自动取价与计算（D-043，前端不传价防伪造）；同 day 重复 → upsert 覆盖（支持纠错）。
func (s *Service) AddDailyIncome(ctx context.Context, req *connect.Request[v1.AddDailyIncomeRequest]) (*connect.Response[v1.AddDailyIncomeResponse], error) {
	m := req.Msg
	day, err := parseDay(m.GetDay())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid day %q: %w", m.GetDay(), err))
	}
	// 后端取实时价：TRXUSDT 最新未陈旧帧（复用陈旧守卫 G3）；无报价 → price/pnl 写 NULL（诚实空态）。
	var price *float64
	snap := s.qbus.Snapshot(ctx, []string{"TRXUSDT"})
	if q, ok := latestQuote(snap["TRXUSDT"], s.freshTTL, time.Now()); ok {
		p := q.Mid()
		price = &p
	}
	// pnl_usd = (entry − 现价) × qty：从 OPEN 的 short_hedge 行算（设计 §4；无报价不算）。
	var pnl *float64
	if price != nil {
		positions, err := s.store.ListPersonalPositions(ctx, "OPEN", 200, 0)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, err)
		}
		for i := range positions {
			p := &positions[i]
			if p.Kind == "short_hedge" && p.EntryPrice != nil && p.Qty != nil {
				v := (*p.EntryPrice - *price) * *p.Qty
				pnl = &v
				break
			}
		}
	}
	id, err := s.store.AddDailyIncome(ctx, day, m.GetEnergyTrx(), m.GetVotingTrx(), m.GetInterestUsd(), price, pnl, m.GetNote())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	// §11 即时预警：查前日（to=day−1，limit 1 取最近前日）→ fundingWarning(本次, 前日)。
	// 前日查询失败按无前日降级（prev=nil 只走 R1）；预警是提示不是门禁，不阻塞提交。
	cur := m.GetInterestUsd()
	var prevPtr *float64
	prevDay := day.AddDate(0, 0, -1)
	if prevRows, err := s.store.ListDailyIncome(ctx, nil, &prevDay, 1, 0); err == nil && len(prevRows) > 0 {
		v := prevRows[0].InterestUSD
		prevPtr = &v
	}
	return connect.NewResponse(&v1.AddDailyIncomeResponse{Id: id, Warning: fundingWarning(&cur, prevPtr, s.fundingDropRatio)}), nil
}

// ListDailyIncome 按 day 倒序返回每日收益；from/to 可选区间；income_usd 后端算（D-043）。
func (s *Service) ListDailyIncome(ctx context.Context, req *connect.Request[v1.ListDailyIncomeRequest]) (*connect.Response[v1.ListDailyIncomeResponse], error) {
	m := req.Msg
	var from, to *time.Time
	if m.GetFrom() != "" {
		t, err := parseDay(m.GetFrom())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid from %q: %w", m.GetFrom(), err))
		}
		from = &t
	}
	if m.GetTo() != "" {
		t, err := parseDay(m.GetTo())
		if err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("invalid to %q: %w", m.GetTo(), err))
		}
		to = &t
	}
	rows, err := s.store.ListDailyIncome(ctx, from, to, int(m.GetLimit()), int(m.GetOffset()))
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	items := make([]*v1.PositionDailyIncome, 0, len(rows))
	for i := range rows {
		item := dailyIncomeToProto(&rows[i])
		// §11 每行预警：day 倒序相邻对算（rows[i] 的 prev = rows[i+1]，更旧行）。
		// 边界：返回集最旧一行（i=len-1）的 prev 在集外 → best-effort（prev=nil，只可能 R1）。
		var prevPtr *float64
		if i+1 < len(rows) {
			v := rows[i+1].InterestUSD
			prevPtr = &v
		}
		item.Warning = fundingWarning(&rows[i].InterestUSD, prevPtr, s.fundingDropRatio)
		items = append(items, item)
	}
	return connect.NewResponse(&v1.ListDailyIncomeResponse{Items: items}), nil
}

// dailyIncomeToProto 组装单条每日收益：income_usd = energy×price + voting×price + interest（price 无值按 0）。
func dailyIncomeToProto(d *pgstore.DailyIncome) *v1.PositionDailyIncome {
	pb := &v1.PositionDailyIncome{
		Id:          d.ID,
		Day:         d.Day.Format("2006-01-02"),
		EnergyTrx:   d.EnergyTrx,
		VotingTrx:   d.VotingTrx,
		InterestUsd: d.InterestUSD,
		Note:        d.Note,
	}
	price := 0.0
	if d.Price != nil {
		pb.Price = *d.Price
		price = *d.Price
	}
	if d.PnlUSD != nil {
		pb.PnlUsd = *d.PnlUSD
	}
	pb.IncomeUsd = d.EnergyTrx*price + d.VotingTrx*price + d.InterestUSD
	return pb
}

// parseDay 严格解析 YYYY-MM-DD。Go time.Parse 对 "01" 占位宽松（接受 2026-8-19），
// 这里 round-trip 校验拒绝非常规格式。
func parseDay(s string) (time.Time, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}, err
	}
	if t.Format("2006-01-02") != s {
		return time.Time{}, fmt.Errorf("day must be YYYY-MM-DD")
	}
	return t, nil
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
