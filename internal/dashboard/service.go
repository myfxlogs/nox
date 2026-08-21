// Package dashboard 实现 DashboardService RPC handler（§8.2）。
package dashboard

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/exec"
	"arb/internal/fact"
	"arb/internal/position"
	"arb/internal/reconcile"
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
	// priceTopic 实时现价广播（D-049）：PersonalMonitor 经 onPrice 发布 → StreamUpdates 扇出 case 7。nil = 未接线（测试直接构造 Service）。
	priceTopic *bus.Topic[position.PositionPrice]
	// oppTopic 新机会推送广播（D-050）：engine 新插入发布 → StreamUpdates 扇出 case 2。nil = 未接线（测试直接构造 Service）。
	oppTopic *bus.Topic[pgstore.Opportunity]
	// bpm Broker 实持仓 monitor（D-052）：ListBrokerPositions 拉 + StreamUpdates case 8 推同源。nil = 未接线（测试直接构造 Service）。
	bpm *BrokerPositionMonitor
	// notionalUSD net_usd 换算的单笔名义（config.OpportunityNotionalUSD，D-053）。0 = 未接线（测试），net_usd=0。
	notionalUSD float64
	// acctMon 账户资产 monitor（D-053）：ListAccounts 拉 + StreamUpdates case 9 推同源。nil = 未接线（测试直接构造 Service）。
	acctMon *AccountMonitor
	// tradeModes 单腿 trade_mode 查询（D-060 F5 执行预检，防裸腿）：canonical+venueKey → mode。
	// nil = 未接线（测试直接构造 Service，预检放行）。main 注入 factCache.TradeMode（不引入 exec→engine 依赖）。
	tradeModes func(canonical, venueKey string) (int64, bool)
	// legContractSize MT 腿合约规格查询（D-060 实施修正② 卖腿等名义反算）：venueKey+venueSymbol → CS。
	// nil = 未接线 → resolveLegs 报错（fail-safe）。main 接 broker_symbol_map.contract_size。
	legContractSize func(venueKey, venueSymbol string) (float64, bool)
	// maxLegImbalance 卖腿名义不平衡上限（百分比，默认 2，D-028/D-060 实施修正②）。
	maxLegImbalance float64
	// legMinLots MT 腿最小手数查询（D-061 量规格预检）：venueKey+venueSymbol → min lots。
	// nil/未知 = 放行（venue 服务端拒单兜底；position #3 实证：qty 0.1 < IC XBRUSD min 1.0 → LEGGED）。
	legMinLots func(venueKey, venueSymbol string) (float64, bool)
	// legVolumeStep MT 腿 volume step 查询（D-070 qty 精度取整）：venueKey+venueSymbol → step。
	// nil/未知 = 用默认 0.01（MT5 手数步长）。
	legVolumeStep func(venueKey, venueSymbol string) (float64, bool)
	// oppContractSize 机会卡 USD 派生用合约规格查询（D-062）：canonical+venueKey → CS。
	// nil = 未接线（测试直接构造 Service）→ USD 字段全 0，前端回退 bps。main 接 store.ContractSizeByCanonical。
	oppContractSize func(canonical, venueKey string) (float64, bool)
}

// New 创建 dashboard 服务。reviewer 为复盘触发闭包（nil 则平仓后不触发）；
// freshTTL 为报价陈旧阈值（个人持仓展示用，传 config.DetectorFreshnessTTL）；
// liqMMR 为个人持仓理论强平维持保证金率（传 config.LiqMMR）；
// fundingDropRatio 为资金费急降预警比例（传 config.FundingDropRatio）；
// priceTopic 为实时现价广播 topic（D-049，传 main 创建的 priceTopic）；
// oppTopic 为新机会站内推送 topic（D-050，传 main 创建的 oppTopic）；
// bpm 为 Broker 实持仓 monitor（D-052，传 main 创建的 bpm）；
// notionalUSD 为 net_usd 换算的单笔名义（D-053，传 config.OpportunityNotionalUSD）；
// acctMon 为账户资产 monitor（D-053，传 main 创建的 acctMon）。
func New(store *pgstore.Store, qbus *bus.QuoteBus, pipeline *exec.Pipeline, bm *BrokerManager, reviewer func(), freshTTL time.Duration, liqMMR float64, fundingDropRatio float64, priceTopic *bus.Topic[position.PositionPrice], oppTopic *bus.Topic[pgstore.Opportunity], bpm *BrokerPositionMonitor, notionalUSD float64, acctMon *AccountMonitor) *Service {
	return &Service{store: store, qbus: qbus, pipeline: pipeline, bm: bm, reviewer: reviewer, freshTTL: freshTTL, liqMMR: liqMMR, fundingDropRatio: fundingDropRatio, priceTopic: priceTopic, oppTopic: oppTopic, bpm: bpm, notionalUSD: notionalUSD, acctMon: acctMon}
}

// Store 返回底层 store（D-070 autotrade engine 用）。
func (s *Service) Store() *pgstore.Store { return s.store }

// AutoClosePosition 平仓（D-070 autotrade engine 自动平仓用，区别于 RPC ClosePosition）。
func (s *Service) AutoClosePosition(ctx context.Context, posID int64) error {
	return s.pipeline.ClosePosition(ctx, posID)
}

// SetTradeModes 注入单腿 trade_mode 查询（D-060 F5）。main 在 factCache 处装配；nil = 预检放行。
func (s *Service) SetTradeModes(f func(canonical, venueKey string) (int64, bool)) {
	s.tradeModes = f
}

// SetLegContractSize 注入 MT 腿合约规格查询（D-060 实施修正②）。nil = 未接线（resolveLegs 报错）。
func (s *Service) SetLegContractSize(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legContractSize = f
}

// SetMaxLegImbalancePct 注入卖腿名义不平衡上限（百分比；≤0 回退默认 2）。
func (s *Service) SetMaxLegImbalancePct(p float64) { s.maxLegImbalance = p }

// SetLegMinLots 注入 MT 腿最小手数查询（D-061）。nil/未知 = 放行。
func (s *Service) SetLegMinLots(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legMinLots = f
}

// SetLegVolumeStep 注入 MT 腿 volume step 查询（D-070 qty 精度取整）。nil/未知 = 用默认 0.01。
func (s *Service) SetLegVolumeStep(f func(venueKey, venueSymbol string) (float64, bool)) {
	s.legVolumeStep = f
}

// SetOppContractSize 注入机会卡 USD 派生用合约规格查询（D-062）。nil = 未接线 → USD 字段全 0（前端回退 bps）。
func (s *Service) SetOppContractSize(f func(canonical, venueKey string) (float64, bool)) {
	s.oppContractSize = f
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
		pbOpps = append(pbOpps, s.oppToProto(&o))
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
	status, msg, posID := s.ExecuteOpportunityInternal(ctx, req.Msg.GetOpportunityId(), req.Msg.GetRequestId(), req.Msg.GetQty())
	return connect.NewResponse(&v1.ExecuteOpportunityResponse{
		PositionId: posID,
		Status:     status,
		Message:    msg,
	}), nil
}

// ExecuteOpportunityInternal 执行机会的核心逻辑（D-070 抽取，供 RPC 和 autotrade engine 复用）。
// 返回 status（EXECUTED/FAILED）、message、positionID（失败时为0）。
func (s *Service) ExecuteOpportunityInternal(ctx context.Context, oppID, requestID string, qty float64) (status, message string, positionID int64) {
	if s.pipeline == nil {
		return "FAILED", "executor not wired", 0
	}
	if qty <= 0 {
		return "FAILED", "invalid quantity", 0
	}
	opps, err := s.store.ListOpportunities(ctx, "", 200, 0)
	if err != nil {
		return "FAILED", fmt.Sprintf("list opportunities: %v", err), 0
	}
	var found *pgstore.Opportunity
	for i := range opps {
		if opps[i].ID == oppID {
			found = &opps[i]
			break
		}
	}
	if found == nil {
		return "FAILED", "opportunity not found or expired", 0
	}
	if !s.pipeline.Revalidate(found.Canonical, found.VenueBuy, found.VenueSell, found.Type, found.GrossBps) {
		return "FAILED", "quote stale or net benefit degraded beyond threshold", 0
	}
	if err := s.checkTradability(found); err != nil {
		return "FAILED", err.Error(), 0
	}
	legs, err := s.resolveLegs(found, qty)
	if err != nil {
		return "FAILED", err.Error(), 0
	}
	result, err := s.pipeline.Execute(ctx, oppID, requestID, legs)
	if err != nil {
		if errors.Is(err, exec.ErrMaxOpenPositions) || errors.Is(err, exec.ErrDuplicateExecution) {
			return "FAILED", err.Error(), 0
		}
		return "FAILED", fmt.Sprintf("execute: %v", err), 0
	}
	// D-070：回填 position 元数据（pipeline.Execute 创建时未填 type/expected_net_bps）
	if result.PositionID > 0 {
		_ = s.store.UpdatePositionMeta(ctx, result.PositionID, found.Type, found.NetBps)
	}
	return result.Status, "", result.PositionID
}

// ExecuteMeanRevert D-070 均值回归直接执行（绕过 detector，autotrade engine 专用）。
// 直接接收 canonical + venueBuy + venueSell + qty，构造 LegSpec 并执行。
// 返回 status、message、positionID。
func (s *Service) ExecuteMeanRevert(ctx context.Context, canonical, venueBuy, venueSell, requestID string, qty float64, openBps float64) (status, message string, positionID int64) {
	if s.pipeline == nil {
		return "FAILED", "executor not wired", 0
	}
	if qty <= 0 {
		return "FAILED", "invalid quantity", 0
	}
	qbuy, okBuy := s.qbus.Latest(canonical, venueBuy)
	qsell, okSell := s.qbus.Latest(canonical, venueSell)
	if !okBuy || !okSell {
		return "FAILED", "buy/sell leg quote unavailable", 0
	}
	csBuy, err := s.legContractSizeOf(venueBuy, qbuy.VenueSymbol)
	if err != nil {
		return "FAILED", fmt.Sprintf("buy CS: %v", err), 0
	}
	csSell, err := s.legContractSizeOf(venueSell, qsell.VenueSymbol)
	if err != nil {
		return "FAILED", fmt.Sprintf("sell CS: %v", err), 0
	}
	sellQty := math.Round(qty*csBuy/csSell*100) / 100
	qty = s.floorToStepQty(venueBuy, qbuy.VenueSymbol, qty)
	sellQty = s.floorToStepQty(venueSell, qsell.VenueSymbol, sellQty)
	// CFD notional cap
	maxNotional := 10000.0
	if isMTVenue(venueBuy) {
		notional := qty * csBuy * qbuy.Ask
		if notional > maxNotional {
			qty = s.floorToStepQtyMin(venueBuy, qbuy.VenueSymbol)
		}
	}
	if isMTVenue(venueSell) {
		notional := sellQty * csSell * qsell.Bid
		if notional > maxNotional {
			sellQty = s.floorToStepQtyMin(venueSell, qsell.VenueSymbol)
		}
	}
	// imbalance 检查
	ref := qbuy.Ask
	notionalBuy := qty * csBuy * ref
	notionalSell := sellQty * csSell * ref
	maxImb := s.maxLegImbalance
	if maxImb <= 0 {
		maxImb = 2.0
	}
	imb := math.Abs(notionalSell-notionalBuy) / notionalBuy
	if notionalBuy <= 0 || imb > maxImb/100 {
		return "FAILED", fmt.Sprintf("leg notional imbalance: buy $%.2f vs sell $%.2f (%.2f%% > %.1f%%)",
			notionalBuy, notionalSell, imb*100, maxImb), 0
	}
	if err := s.checkLegVolume(venueBuy, qbuy.VenueSymbol, qty); err != nil {
		return "FAILED", err.Error(), 0
	}
	if err := s.checkLegVolume(venueSell, qsell.VenueSymbol, sellQty); err != nil {
		return "FAILED", err.Error(), 0
	}
	oppID := fmt.Sprintf("MEANREVERT:%s:%s:%s:%d", canonical, venueBuy, venueSell, time.Now().UnixMilli())
	legs := []exec.LegSpec{
		{VenueKey: venueBuy, VenueSymbol: qbuy.VenueSymbol, Side: "BUY", Qty: qty, PriceExpected: qbuy.Ask},
		{VenueKey: venueSell, VenueSymbol: qsell.VenueSymbol, Side: "SELL", Qty: sellQty, PriceExpected: qsell.Bid},
	}
	result, err := s.pipeline.Execute(ctx, oppID, requestID, legs)
	if err != nil {
		if errors.Is(err, exec.ErrMaxOpenPositions) || errors.Is(err, exec.ErrDuplicateExecution) {
			return "FAILED", err.Error(), 0
		}
		return "FAILED", fmt.Sprintf("execute: %v", err), 0
	}
	if result.PositionID > 0 {
		_ = s.store.UpdatePositionMeta(ctx, result.PositionID, "CROSS_VENUE", openBps)
	}
	return result.Status, "", result.PositionID
}

// checkTradability F5 执行预检：MT 腿（venueKey 后缀 :mt5/:mt4）查 trade_mode，
// 买腿 mode∉{LongOnly,Full} / 卖腿 mode∉{ShortOnly,Full} → 返回错误（含 venue+符号+mode），
// 调用方拒绝执行不建 position；CEX 腿跳过；未知（无 Fact/TTL 过期）放行（venue 服务端拒单是最终权威）。
func (s *Service) checkTradability(found *pgstore.Opportunity) error {
	if s.tradeModes == nil {
		return nil
	}
	for _, leg := range []struct {
		venueKey string
		buy      bool
	}{{found.VenueBuy, true}, {found.VenueSell, false}} {
		if !strings.HasSuffix(leg.venueKey, ":mt5") && !strings.HasSuffix(leg.venueKey, ":mt4") {
			continue // CEX 腿：trade_mode 仅 MT 语义
		}
		mode, ok := s.tradeModes(found.Canonical, leg.venueKey)
		if !ok {
			continue
		}
		if leg.buy && mode != fact.TradeModeLongOnly && mode != fact.TradeModeFull {
			return fmt.Errorf("buy leg %s %s not tradable (trade mode %d)", leg.venueKey, found.Canonical, mode)
		}
		if !leg.buy && mode != fact.TradeModeShortOnly && mode != fact.TradeModeFull {
			return fmt.Errorf("sell leg %s %s not tradable (trade mode %d)", leg.venueKey, found.Canonical, mode)
		}
	}
	return nil
}

// resolveLegs 从 qbus 取双腿 broker 原生符号构造下单规格（D-051 修②）。
// canonical 用机会列（非 ID 解析）；缺任一腿报价 → 返回错误（执行方 FAILED 透传）。
// D-060 实施修正②（D-028 补课）：卖腿等名义反算 sellQty = qty×CS_buy/CS_sell（0.01 取整）
// + 名义不平衡拒绝 + PriceExpected 落腿。实证：position #2 油 1000:100 桶/手同 qty 0.1
// 发卖腿被 IC 拒「Invalid volume」→ LEGGED（等名义反算 D-028 设计了但从未实现）。
func (s *Service) resolveLegs(found *pgstore.Opportunity, qty float64) ([]exec.LegSpec, error) {
	qbuy, okBuy := s.qbus.Latest(found.Canonical, found.VenueBuy)
	qsell, okSell := s.qbus.Latest(found.Canonical, found.VenueSell)
	if !okBuy || !okSell {
		return nil, errors.New("buy/sell leg quote unavailable")
	}
	csBuy, err := s.legContractSizeOf(found.VenueBuy, qbuy.VenueSymbol)
	if err != nil {
		return nil, err
	}
	csSell, err := s.legContractSizeOf(found.VenueSell, qsell.VenueSymbol)
	if err != nil {
		return nil, err
	}
	sellQty := math.Round(qty*csBuy/csSell*100) / 100
	// D-070：qty 精度对齐（Binance -1111 / MT5 Invalid volume 修复）
	// MT5 取整后为 0 会回退到 1 step（最小可交易量），不会返回 0
	qty = s.floorToStepQty(found.VenueBuy, qbuy.VenueSymbol, qty)
	sellQty = s.floorToStepQty(found.VenueSell, qsell.VenueSymbol, sellQty)
	// D-070 CFD 保证金保护：MT5 品种 CS 可能很大（BRENT=1000），
	// 取整后名义额 = qty * CS * price 可能远超账户余额。
	// 若买腿名义 > MaxNotional × 10（套利两腿对冲，保证金只需名义的一小部分），
	// 降到 1 个 volume step（最小可交易量）。
	maxNotional := 10000.0 // $10K 名义上限（CFD 保证金通常 1-5%，两腿对冲后更低）
	if isMTVenue(found.VenueBuy) {
		notional := qty * csBuy * qbuy.Ask
		if notional > maxNotional {
			qty = s.floorToStepQtyMin(found.VenueBuy, qbuy.VenueSymbol)
			log.Printf("[exec] CFD notional cap: buy %s qty %.4f→%.4f (notional $%.0f > $%.0f)",
				found.VenueBuy, qty, qty, notional, maxNotional)
		}
	}
	if isMTVenue(found.VenueSell) {
		notional := sellQty * csSell * qsell.Bid
		if notional > maxNotional {
			sellQty = s.floorToStepQtyMin(found.VenueSell, qsell.VenueSymbol)
			log.Printf("[exec] CFD notional cap: sell %s qty %.4f→%.4f (notional $%.0f > $%.0f)",
				found.VenueSell, sellQty, sellQty, notional, maxNotional)
		}
	}
	// 不平衡口径修正：按买腿参考价统一计两腿名义——D-028 防的是「取整导致的数量差」，
	// 不是两腿各自可执行价的差（那是套利缺口本身：等量对冲腿在不同 venue 价格下名义额必然不同，
	// 3.3% 价差曾把正确订单误拒为 imbalance）。
	ref := qbuy.Ask
	notionalBuy := qty * csBuy * ref
	notionalSell := sellQty * csSell * ref
	maxImb := s.maxLegImbalance
	if maxImb <= 0 {
		maxImb = 2.0
	}
	imb := math.Abs(notionalSell-notionalBuy) / notionalBuy
	if notionalBuy <= 0 || imb > maxImb/100 {
		return nil, fmt.Errorf("leg notional imbalance: buy $%.2f vs sell $%.2f (%.2f%% > %.1f%%)",
			notionalBuy, notionalSell, imb*100, maxImb)
	}
	// 量规格预检（D-061）：双腿 qty 不得低于 venue 最小手数（position #3 实证形态）。
	if err := s.checkLegVolume(found.VenueBuy, qbuy.VenueSymbol, qty); err != nil {
		return nil, err
	}
	if err := s.checkLegVolume(found.VenueSell, qsell.VenueSymbol, sellQty); err != nil {
		return nil, err
	}
	return []exec.LegSpec{
		{VenueKey: found.VenueBuy, VenueSymbol: qbuy.VenueSymbol, Side: "BUY", Qty: qty, PriceExpected: qbuy.Ask},
		{VenueKey: found.VenueSell, VenueSymbol: qsell.VenueSymbol, Side: "SELL", Qty: sellQty, PriceExpected: qsell.Bid},
	}, nil
}

// checkLegVolume D-061 量规格预检：qty < venue 最小手数 → 拒（下单前，防单腿被拒 LEGGED）。
// CEX 腿跳过；解析器未接线/未知 → 放行（服务端拒单兜底）。
func (s *Service) checkLegVolume(venueKey, venueSymbol string, qty float64) error {
	if !strings.HasSuffix(venueKey, ":mt5") && !strings.HasSuffix(venueKey, ":mt4") {
		return nil
	}
	if s.legMinLots == nil {
		return nil
	}
	min, ok := s.legMinLots(venueKey, venueSymbol)
	if !ok || min <= 0 || qty >= min {
		return nil
	}
	return fmt.Errorf("qty %v below venue min lot %.2f for %s %s", qty, min, venueKey, venueSymbol)
}

// legContractSizeOf 单腿合约规格：CEX 腿 CS=1（qty 即基础币数）；MT 腿经 legContractSize
// 注入查询（main 接 broker_symbol_map.contract_size，D-042 backfilled）。缺失/未接线 →
// 报错拒不猜（fail-safe，防 position #2 形态复发）。
func (s *Service) legContractSizeOf(venueKey, venueSymbol string) (float64, error) {
	if !strings.HasSuffix(venueKey, ":mt5") && !strings.HasSuffix(venueKey, ":mt4") {
		return 1.0, nil
	}
	if s.legContractSize == nil {
		return 0, errors.New("leg contract size resolver not wired")
	}
	cs, ok := s.legContractSize(venueKey, venueSymbol)
	if !ok || cs <= 0 {
		return 0, fmt.Errorf("contract size unknown for %s %s (reject: notional derivation unavailable)", venueKey, venueSymbol)
	}
	return cs, nil
}

// isMTVenue 判断是否为 MT5/MT4 venue。
func isMTVenue(venue string) bool {
	return strings.HasSuffix(venue, ":mt5") || strings.HasSuffix(venue, ":mt4")
}

// floorToStepQtyMin 返回 MT5 品种的最小可交易量（1 个 volume step）。
func (s *Service) floorToStepQtyMin(venueKey, symbol string) float64 {
	if s.legVolumeStep != nil {
		if vs, ok := s.legVolumeStep(venueKey, symbol); ok && vs > 0 {
			return vs
		}
	}
	return 0.01
}

// floorToStepQty 按品种精度向下取整 qty（D-070 修复 Binance -1111 / MT5 Invalid volume）。
// MT 腿：查 legVolumeStep（SymbolParams VolumeStep），未接线/未知用默认 0.01。
// D-070 CFD 适配：取整后为 0 则回退到 1 个 step（最小可交易量），防 qty=0 无法交易。
// CEX 腿：按品种 stepSize 取整；未知品种默认 0.01。
func (s *Service) floorToStepQty(venueKey, symbol string, qty float64) float64 {
	if strings.HasSuffix(venueKey, ":mt5") || strings.HasSuffix(venueKey, ":mt4") {
		step := 0.01 // 默认 MT5 手数步长
		if s.legVolumeStep != nil {
			if vs, ok := s.legVolumeStep(venueKey, symbol); ok && vs > 0 {
				step = vs
			}
		}
		result := math.Floor(qty/step) * step
		if result <= 0 {
			result = step // 回退到最小可交易量 1 step
		}
		log.Printf("[exec] floorToStepQty mt %s %s qty=%.6f step=%.4f → %.6f", venueKey, symbol, qty, step, result)
		return result
	}
	// CEX：按品种 stepSize
	step := cexStepSize(symbol)
	result := math.Floor(qty/step) * step
	log.Printf("[exec] floorToStepQty cex %s %s qty=%.6f step=%.6f → %.6f", venueKey, symbol, qty, step, result)
	return result
}

// cexStepSize 返回 CEX 品种的 stepSize（向下取整单位）。
// 已知品种用实测值；未知品种默认 0.01（2位小数，多数山寨币）。
func cexStepSize(symbol string) float64 {
	// 提取基础币种（去 USDT/USD 后缀）
	base := symbol
	for _, suf := range []string{"USDT", "USD", "-USDT", "-USD", "-SWAP"} {
		base = strings.TrimSuffix(base, suf)
	}
	switch base {
	case "BTC":
		return 0.001 // binance spot 0.00001, perp 0.001；取保守 0.001
	case "ETH":
		return 0.001 // binance spot 0.0001, perp 0.001
	case "TRX":
		return 1 // binance spot 1, perp 1
	case "SOL":
		return 0.01
	case "BNB":
		return 0.01
	case "XRP":
		return 1
	case "DOGE":
		return 1
	case "ADA":
		return 1
	case "MATIC":
		return 1
	case "DOT":
		return 0.01
	case "LINK":
		return 0.01
	case "LTC":
		return 0.001
	case "AVAX":
		return 0.01
	default:
		return 0.01 // 未知品种保守 2 位小数
	}
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

// --- Broker 实持仓（D-051 WP3：MT5/MT4 实际持仓统一视图，实时查不落库） ---

// ListBrokerPositions 返回 Broker 实持仓视图（D-052 monitor 单一真相源）。
// refresh=true 强制触发一次 venue 轮询后返回（前端「刷新」/平仓后立即刷新）；否则返回最近缓存（≤ 轮询周期）。
// canonical 解析 + 现价计算均在后端（D-043 零信任：前端只渲染）；离线 venue 保留最近快照并 venue_online=false。
func (s *Service) ListBrokerPositions(ctx context.Context, req *connect.Request[v1.ListBrokerPositionsRequest]) (*connect.Response[v1.ListBrokerPositionsResponse], error) {
	if s.bpm == nil {
		return connect.NewResponse(&v1.ListBrokerPositionsResponse{}), nil
	}
	var out []*v1.BrokerPosition
	if req.Msg.GetRefresh() {
		out = s.bpm.Refresh(ctx)
	} else {
		out = s.bpm.Snapshot()
	}
	return connect.NewResponse(&v1.ListBrokerPositionsResponse{Positions: out}), nil
}

// CloseBrokerPosition 经注册执行器平仓一条 venue 持仓（人工确认后，§1）。
func (s *Service) CloseBrokerPosition(ctx context.Context, req *connect.Request[v1.CloseBrokerPositionRequest]) (*connect.Response[v1.CloseBrokerPositionResponse], error) {
	if _, err := s.bm.CloseOrder(ctx, req.Msg.GetVenueKey(), req.Msg.GetTicket()); err != nil {
		return connect.NewResponse(&v1.CloseBrokerPositionResponse{Status: "FAILED", Message: err.Error()}), nil
	}
	return connect.NewResponse(&v1.CloseBrokerPositionResponse{Status: "CLOSED"}), nil
}

// CloseBrokerPositions 批量平仓（D-052 全平选中）：逐条执行，单条失败不阻断后续。
// 前端二次点击确认后调用；每项独立返回 CLOSED/FAILED。
func (s *Service) CloseBrokerPositions(ctx context.Context, req *connect.Request[v1.CloseBrokerPositionsRequest]) (*connect.Response[v1.CloseBrokerPositionsResponse], error) {
	items := req.Msg.GetItems()
	results := make([]*v1.CloseBrokerPositionResponse, 0, len(items))
	for _, it := range items {
		if _, err := s.bm.CloseOrder(ctx, it.GetVenueKey(), it.GetTicket()); err != nil {
			results = append(results, &v1.CloseBrokerPositionResponse{Status: "FAILED", Message: err.Error()})
			continue
		}
		results = append(results, &v1.CloseBrokerPositionResponse{Status: "CLOSED"})
	}
	return connect.NewResponse(&v1.CloseBrokerPositionsResponse{Results: results}), nil
}

// --- 账户资产（D-053） ---

// ListAccounts 返回账户资产快照（acctMon 单一真相源；≤ 轮询周期的缓存，无需强制刷新）。
func (s *Service) ListAccounts(ctx context.Context, req *connect.Request[v1.ListAccountsRequest]) (*connect.Response[v1.ListAccountsResponse], error) {
	if s.acctMon == nil {
		return connect.NewResponse(&v1.ListAccountsResponse{}), nil
	}
	return connect.NewResponse(&v1.ListAccountsResponse{Accounts: s.acctMon.Snapshot()}), nil
}

// brokerPositionToProto 组装单个 venue 持仓响应。
// current_price：canonical 已解析且报价帧新鲜（now.Sub ≤ freshTTL）→ mid，fresh=true；否则 0/false（诚实空态）。
// is_arb：comment 前缀 = exec.OrderComment（复用 reconcile.isARBOrder 语义，D-032）。
// pnl 为 venue 权威浮盈（Order.Profit）透传，前端不自算（D-043）。
// venueOnline=false = 离线 venue 的最近快照（非实时，诚实标注，D-052）。
func brokerPositionToProto(o reconcile.VenueOrder, canonical string, price bus.Quote, hasPrice bool, venueOnline bool, freshTTL time.Duration, now time.Time) *v1.BrokerPosition {
	pb := &v1.BrokerPosition{
		Venue:       o.Venue,
		VenueKey:    o.VenueKey,
		Ticket:      o.Ticket,
		Symbol:      o.Symbol,
		Canonical:   canonical,
		Side:        o.Side,
		Volume:      o.Volume,
		OpenPrice:   o.OpenPrice,
		Pnl:         o.Profit,
		Swap:        o.Swap,
		Commission:  o.Commission,
		Comment:     o.Comment,
		IsArb:       strings.HasPrefix(o.Comment, exec.OrderComment),
		Magic:       o.Magic,
		VenueOnline: venueOnline,
	}
	if hasPrice && now.Sub(price.Time) <= freshTTL {
		pb.CurrentPrice = (price.Bid + price.Ask) / 2
		pb.Fresh = true
	}
	if !o.OpenedAt.IsZero() {
		pb.OpenedAt = timestamppb.New(o.OpenedAt)
	}
	return pb
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
	// D-069：total 用于前端分页控件
	total, err := s.store.CountPositions(ctx, req.Msg.GetStatus())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	var pbPositions []*v1.Position
	for _, p := range positions {
		pb := positionToProto(&p)
		// D-069：填充 legs（成交历史，OPEN + CLOSE phase）
		legs, err := s.store.ListPositionLegs(ctx, p.ID)
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("list legs for position %d: %w", p.ID, err))
		}
		for _, l := range legs {
			pb.Legs = append(pb.Legs, positionLegToProto(&l))
		}
		pbPositions = append(pbPositions, pb)
	}
	return connect.NewResponse(&v1.ListPositionsResponse{Positions: pbPositions, Total: int32(total)}), nil
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

	// 价格派生字段：单点计算（D-049，P3：快照路径与实时推送共用同一 ComputePositionPrice）。
	v := position.ComputePositionPrice(p, venues, freshTTL, now, liqMMR)
	pb.Price = v.Price
	pb.Level = v.Level
	pb.PnlUsd = v.PnlUsd
	pb.LiqPrice = v.LiqPrice
	pb.DistToLiqPct = v.DistToLiqPct
	return pb
}

// positionPriceToProto 实时现价推送消息（D-049）：前端仅按 id 合并渲染，不计算。
func positionPriceToProto(v position.PositionPrice) *v1.PersonalPositionPrice {
	return &v1.PersonalPositionPrice{
		PositionId:   v.ID,
		Price:        v.Price,
		Level:        v.Level,
		PnlUsd:       v.PnlUsd,
		LiqPrice:     v.LiqPrice,
		DistToLiqPct: v.DistToLiqPct,
	}
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
	if q, ok := bus.LatestQuote(snap["TRXUSDT"], s.freshTTL, time.Now()); ok {
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
	// 机会（D-050）/告警/持仓由引擎 30s 节流产生：新机会经 oppTopic 实时推 case 2；
	// 告警/持仓仍由前端 30s 低频快照补齐。
	ch, cancel := s.qbus.Subscribe("")
	defer cancel()
	// 实时个人持仓现价（D-049）：PersonalMonitor 广播 → 逐仓推 case 7（与 quote 流并存，独立 Topic 扇出）。
	// priceTopic 可能为 nil（测试直接构造 Service 未接线）→ 不订阅该分支。
	var ppCh <-chan position.PositionPrice
	if s.priceTopic != nil {
		var ppCancel func()
		ppCh, ppCancel = s.priceTopic.Subscribe()
		defer ppCancel()
	}
	// 实时新机会推送（D-050）：engine 新插入发布 → 逐条推 case 2（与 quote/现价流并存，独立 Topic 扇出）。
	// oppTopic 可能为 nil（测试直接构造 Service 未接线）→ 不订阅该分支。
	var oppCh <-chan pgstore.Opportunity
	if s.oppTopic != nil {
		var oppCancel func()
		oppCh, oppCancel = s.oppTopic.Subscribe()
		defer oppCancel()
	}
	// Broker 实持仓推送（D-052）：monitor 变更广播全量快照 → case 8。bpm 为 nil（测试未接线）→ 不订阅。
	var bpCh <-chan *v1.BrokerPositionSnapshot
	if s.bpm != nil && s.bpm.topic != nil {
		var bpCancel func()
		bpCh, bpCancel = s.bpm.topic.Subscribe()
		defer bpCancel()
	}
	// 账户资产推送（D-053）：monitor 变更广播全量快照 → case 9。acctMon 为 nil（测试未接线）→ 不订阅。
	var accCh <-chan *v1.AccountSnapshot
	if s.acctMon != nil && s.acctMon.topic != nil {
		var accCancel func()
		accCh, accCancel = s.acctMon.topic.Subscribe()
		defer accCancel()
	}
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
		case v, ok := <-ppCh:
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_PersonalPositionPrice{
					PersonalPositionPrice: positionPriceToProto(v),
				},
			}); err != nil {
				return err
			}
		case v, ok := <-oppCh:
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_Opportunity{
					Opportunity: s.oppToProto(&v),
				},
			}); err != nil {
				return err
			}
		case v, ok := <-bpCh:
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_BrokerPositions{
					BrokerPositions: v,
				},
			}); err != nil {
				return err
			}
		case v, ok := <-accCh:
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_Accounts{
					Accounts: v,
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

// netUSD 净收益 bps → USD（D-053）。与 net_bps 同语义：价差型=一次性（/10000×notional），
// 息差型=年化 USD/年（/100×notional，net_bps 为年化百分点，D-036）。notional≤0 时不换算（0）。
func netUSD(netBps float64, typ string, notional float64) float64 {
	if notional <= 0 {
		return 0
	}
	if strings.HasPrefix(typ, "CROSS_VENUE") {
		return netBps / 10000 * notional
	}
	return netBps / 100 * notional
}

func (s *Service) oppToProto(o *pgstore.Opportunity) *v1.Opportunity {
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
		NetUsd:         netUSD(o.NetBps, o.Type, s.notionalUSD),
		NotionalUsd:    s.notionalUSD,
	}
	// D-062 显示层 USD 派生（存储不动）。gap_usd_per_unit 无 CS 依赖恒填；
	// per-lot 字段依赖 venue_buy 侧 CS（D-061 对称桶保证跨 venue 一致），缺失/≤0 → 全 0（前端回退 bps）。
	pb.GapUsdPerUnit = o.PriceSell - o.PriceBuy
	if s.oppContractSize != nil {
		if cs, ok := s.oppContractSize(o.Canonical, o.VenueBuy); ok && cs > 0 && o.PriceBuy > 0 {
			totalCost := o.SpreadCost + o.CommissionCost + o.SlippageCost + o.SwapCost
			pb.CostUsdPerLot = totalCost / 1e4 * cs * o.PriceBuy
			if isCarryType(o.Type) {
				// 息差型：net_bps 为年化百分点 → USD/日/手 = net_bps/100 × CS × price / 365。
				pb.NetUsdPerLotDay = o.NetBps / 100 * cs * o.PriceBuy / 365
			} else {
				// 价差型：net_bps 为一次性 bps → USD/手 = net_bps/1e4 × CS × price。
				pb.NetUsdPerLot = o.NetBps / 1e4 * cs * o.PriceBuy
			}
		}
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

// isCarryType 息差型判定（CARRY / FUNDING_HEDGE），与 netUSD 的 CROSS_VENUE 分支互补（D-062）。
func isCarryType(typ string) bool {
	return strings.HasPrefix(typ, "CARRY") || strings.HasPrefix(typ, "FUNDING_HEDGE")
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

// positionLegToProto 将 position_legs 行转为 proto（D-069 成交历史）。
func positionLegToProto(l *pgstore.PositionLeg) *v1.PositionLeg {
	pb := &v1.PositionLeg{
		Id:            l.ID,
		PositionId:    l.PositionID,
		LegNo:         int32(l.LegNo),
		Phase:         l.Phase,
		Venue:         l.Venue,
		Account:       l.Account,
		Symbol:        l.Symbol,
		Side:          l.Side,
		Qty:           l.Qty,
		PriceExpected: l.PriceExpected,
		OrderId:       l.OrderID,
		Status:        l.Status,
		Ts:            timestamppb.New(l.Ts),
	}
	if l.PriceActual != nil {
		pb.PriceActual = l.PriceActual
	}
	return pb
}
