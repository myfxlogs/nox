package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/data/bus"
	"arb/internal/store/pgstore"

	v1 "arb/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// --- 实时推送（§8.2 D-026） ---

func (s *Service) StreamUpdates(ctx context.Context, req *connect.Request[v1.StreamUpdatesRequest], stream *connect.ServerStream[v1.StreamUpdatesResponse]) error {
	// 实时推送报价：订阅 QuoteBus 全量（wildcard ""），亚秒级行情走推流。
	ch, cancel := s.qbus.Subscribe("")
	defer cancel()
	// 实时新机会推送（D-050）：engine 新插入发布 → 逐条推 case 2。oppTopic nil=不订阅。
	var oppCh <-chan pgstore.Opportunity
	if s.oppTopic != nil {
		var oppCancel func()
		oppCh, oppCancel = s.oppTopic.Subscribe()
		defer oppCancel()
	}
	// Broker 实持仓推送（D-052 → D-571 包A 逐仓事件化）：monitor 变更广播逐仓事件 → case 15。
	// case 8 快照源已随 monitor 事件化移除（前端 keyed merge 走 case 15）。bpm nil=不订阅。
	var bpCh <-chan *v1.BrokerPositionEvent
	if s.bpm != nil && s.bpm.evTopic != nil {
		var bpCancel func()
		bpCh, bpCancel = s.bpm.evTopic.Subscribe()
		defer bpCancel()
	}
	// 账户资产推送（D-571 包B 事件化）：逐 venue 变更事件 → case 16。acctMon nil=不订阅。
	var accCh <-chan *v1.AccountEvent
	if s.acctMon != nil && s.acctMon.evTopic != nil {
		var accCancel func()
		accCh, accCancel = s.acctMon.evTopic.Subscribe()
		defer accCancel()
	}
	// 资金注入提醒推送（D-072）：engine 余额不足发布 → 逐条推 case 10。fundingNeedTopic nil=不订阅。
	var fnCh <-chan FundingNeedNotice
	if s.fundingNeedTopic != nil {
		var fnCancel func()
		fnCh, fnCancel = s.fundingNeedTopic.Subscribe()
		defer fnCancel()
	}
	// D-114：告警实时推送（InsertAlert → store.alertHook → alertTopic → case 3）。nil=不订阅。
	var alertCh <-chan pgstore.Alert
	if s.alertTopic != nil {
		var alertCancel func()
		alertCh, alertCancel = s.alertTopic.Subscribe()
		defer alertCancel()
	}
	// D-571 包C：posMon 双域订阅——positions 行级 case4 复活 /
	// sourceHealth 行级 case5 复活（case 11/12 通知型已退役，通知→重取模式全部收口为
	// 行级/聚合载荷帧；D-063：personal case18 随渲染面退役拆除；hedge case17 随族件剔除拆除）。posMon nil=不订阅。
	posCh := s.posMon.SubscribePositions()
	healthCh := s.posMon.SubscribeHealth()
	refreshCh, refreshCancel := s.subscribeRefreshTopic() // D-508 S9：域刷新通知 → case 14
	defer refreshCancel()
	ticker := time.NewTicker(30 * time.Second) // keepalive 每 30s（§8.2 D-027）
	defer ticker.Stop()
	// D-090 自选报价条：case 1 蓄压 flush ticker（默认 1s，测试可注 10ms）。per (venueKey|symbol) 蓄压只推最新一帧。
	flushInterval := s.quoteFlushInterval
	if flushInterval <= 0 {
		flushInterval = time.Second
	}
	flushTicker := time.NewTicker(flushInterval)
	defer flushTicker.Stop()
	// D-090：流启动初始化——首帧快照（AllLatest 按 watchlist 过滤逐条 Send）。D-508 S9 拆 streamInitQuotes。
	if err := s.streamInitQuotes(ctx, stream); err != nil {
		return err
	}
	// 回放=各域最新态逐条 upsert（包A/B/C 域+opp/alert；新连接自愈，前端零水合 RPC）。
	if err := s.streamInitPositionsAndAccounts(ctx, stream); err != nil {
		return err
	}
	pending := make(map[string]bus.Quote) // D-090：每连接独立，key=venueKey|symbol，覆盖式=自动最新值
	for {
		select {
		case <-ctx.Done():
			return nil
		case q, ok := <-ch:
			if !ok {
				return nil
			}
			// D-090 R1：双维命中检查——symbol∈set 且（venues 空 或 venueKey∈venues）。未命中丢弃。
			if !s.watchlistHitQuote(q) {
				continue
			}
			pending[q.VenueKey()+"|"+q.Symbol] = q // 蓄压不 Send，等 flushTicker 周期 flush
		case <-flushTicker.C: // D-090：遍历 pending 逐条 Send 后清 map（N 帧只出最新一帧）
			for k, q := range pending {
				if err := stream.Send(&v1.StreamUpdatesResponse{
					Update: &v1.StreamUpdatesResponse_Quote{
						Quote: quoteToProto(q),
					},
				}); err != nil {
					return err
				}
				delete(pending, k)
			}
		case v, ok := <-oppCh:
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_Opportunity{
					Opportunity: s.oppToProto(ctx, &v),
				},
			}); err != nil {
				return err
			}
		case v, ok := <-bpCh:
			if !ok {
				return nil
			}
			// D-571 包A：载荷已在 monitor flush 标注（annotateFn 同源三件套），此处直接透传。
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_BrokerPositionEvent{
					BrokerPositionEvent: v,
				},
			}); err != nil {
				return err
			}
		case v, ok := <-accCh:
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_AccountEvent{
					AccountEvent: v,
				},
			}); err != nil {
				return err
			}
		case v, ok := <-fnCh:
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_FundingNeed{
					FundingNeed: fundingNeedToProto(v),
				},
			}); err != nil {
				return err
			}
		case v, ok := <-posCh: // D-571 包C case 4 复活：行级 upsert
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_Position{Position: v},
			}); err != nil {
				return err
			}
		case v, ok := <-healthCh: // case 5 复活：sourceHealth 行级
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_SourceHealth{SourceHealth: v},
			}); err != nil {
				return err
			}
		case a, ok := <-alertCh:
			if !ok {
				return nil
			}
			// D-114：告警实时推送——全量 Alert 对象，前端按 id 合并（同 opportunity case 2 模式）。
			if err := stream.Send(&v1.StreamUpdatesResponse{
				Update: &v1.StreamUpdatesResponse_Alert{
					Alert: alertToProto(&a),
				},
			}); err != nil {
				return err
			}
		case topic, ok := <-refreshCh:
			// D-508 S9：topic 透传——前端按订阅 topic 集重取对应 RPC（复用 case 11 通知→重取模式）。
			if !ok {
				return nil
			}
			if err := stream.Send(&v1.StreamUpdatesResponse{Update: &v1.StreamUpdatesResponse_DataRefresh{DataRefresh: &v1.DataRefreshNotice{Topic: topic}}}); err != nil {
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

// SetFrontConfig 注入前端展示阈值（D-077 修复⑪B N7，main 装配后调）。
func (s *Service) SetFrontConfig(cfg FrontConfig) { s.frontCfg = cfg }

// GetFrontConfig 返回后端 config 阈值供前端展示同源（D-077 修复⑪B N7）。
func (s *Service) GetFrontConfig(ctx context.Context, req *connect.Request[v1.GetFrontConfigRequest]) (*connect.Response[v1.GetFrontConfigResponse], error) {
	return connect.NewResponse(&v1.GetFrontConfigResponse{
		NotifyMinNetBps:   s.frontCfg.NotifyMinNetBps,
		FreshnessTtlSec:   int32(s.frontCfg.FreshnessTTLSec),
		EntrySpreadMinBps: s.frontCfg.EntrySpreadMinBps,
		MinNetSwapBps:     s.frontCfg.MinNetSwapBps,
		RevalidateAbsBps:  s.frontCfg.RevalidateAbsBps,
		RevalidateRelPct:  s.frontCfg.RevalidateRelPct,
	}), nil
}

// GetStrategyLedger D-080 S5：理论 vs 实际对账——策略聚合卡。
// actual_annual_pct = realized_pnl / (notional × hold_days) × 365（估算，notional 取配置默认值）。
func (s *Service) GetStrategyLedger(ctx context.Context, req *connect.Request[v1.GetStrategyLedgerRequest]) (*connect.Response[v1.GetStrategyLedgerResponse], error) {
	aggs, err := s.store.GetStrategyAgg(ctx, req.Msg.Strategy)
	if err != nil {
		return nil, fmt.Errorf("get strategy agg: %w", err)
	}
	entries := make([]*v1.StrategyLedgerEntry, 0, len(aggs))
	for _, a := range aggs {
		entry := &v1.StrategyLedgerEntry{
			Strategy:       a.Strategy,
			TotalPositions: int32(a.TotalPositions),
			OpenPositions:  int32(a.OpenPositions),
			RealizedPnlUsd: a.RealizedPnlUsd,
			AvgHoldDays:    a.AvgHoldDays,
		}
		if a.PromisedAnnualPct != nil {
			entry.PromisedAnnualPct = *a.PromisedAnnualPct
		}
		// 实际年化估算：realized_pnl / (notional × hold_days) × 365（notional 取配置默认值）
		if a.AvgHoldDays > 0 && s.notionalUSD > 0 && a.TotalPositions > 0 {
			notionalTotal := s.notionalUSD * float64(a.TotalPositions)
			entry.ActualAnnualPct = a.RealizedPnlUsd / (notionalTotal * a.AvgHoldDays) * 365 * 100
		}
		entries = append(entries, entry)
	}
	return connect.NewResponse(&v1.GetStrategyLedgerResponse{Entries: entries}), nil
}
