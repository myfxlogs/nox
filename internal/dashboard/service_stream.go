package dashboard

import (
	"context"
	"fmt"
	"time"

	"arb/internal/bus"
	"arb/internal/position"
	"arb/internal/store/pgstore"

	v1 "arb/internal/dashboard/gen/arb/dashboard/v1"

	"connectrpc.com/connect"
)

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
	// 资金注入提醒推送（D-072）：engine 余额不足发布 → 逐条推 case 10。fundingNeedTopic 为 nil（测试未接线）→ 不订阅。
	var fnCh <-chan FundingNeedNotice
	if s.fundingNeedTopic != nil {
		var fnCancel func()
		fnCh, fnCancel = s.fundingNeedTopic.Subscribe()
		defer fnCancel()
	}
	// keepalive 每 30s（§8.2 D-027）
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	// D-090 自选报价条：case 1 蓄压 flush ticker（默认 1s，测试可注 10ms）。
	// 治吵三刀之一——per (venueKey|symbol) 蓄压只推最新一帧，亚秒裸推 → 1s 节流。
	flushInterval := s.quoteFlushInterval
	if flushInterval <= 0 {
		flushInterval = time.Second
	}
	flushTicker := time.NewTicker(flushInterval)
	defer flushTicker.Stop()
	// D-090：流启动初始化——缓存未初始化则先查库填缓存（治 App 根流先于任何 GetWatchlist
	// 启动时快照空+过滤失效；每次连接至多一查）。
	if err := s.ensureWatchlistLoaded(ctx); err != nil {
		return fmt.Errorf("stream init watchlist: %w", err)
	}
	// D-090 首帧快照：订阅建立后、进 select 前，AllLatest 按 watchlist 过滤逐条 Send
	// （秒开不等 tick；与 ListLatestQuotes 同源 quoteToProto）。
	// 返修包① R1：过滤改用 watchlistHitQuote（双维：symbol+venueKey）。
	for _, q := range s.qbus.AllLatest() {
		if !s.watchlistHitQuote(q) {
			continue
		}
		if err := stream.Send(&v1.StreamUpdatesResponse{
			Update: &v1.StreamUpdatesResponse_Quote{
				Quote: quoteToProto(q),
			},
		}); err != nil {
			return err
		}
	}
	// pending 每连接独立（函数栈上），key=q.VenueKey()+"|"+q.Symbol，覆盖式=自动最新值。
	pending := make(map[string]bus.Quote)
	for {
		select {
		case <-ctx.Done():
			return nil
		case q, ok := <-ch:
			if !ok {
				return nil
			}
			// D-090 返修包① R1：RLock 双维命中检查——symbol∈set 且（venues 空 或 venueKey∈venues）。
			// 未命中丢弃（治吵三刀之一：只推自选）。
			if !s.watchlistHitQuote(q) {
				continue
			}
			// 蓄压不 Send（复用 VenueKey()，勿手拼），等 flushTicker 周期 flush。
			pending[q.VenueKey()+"|"+q.Symbol] = q
		case <-flushTicker.C:
			// D-090：遍历 pending 逐条 Send 后清 map（N 帧只出最新一帧）。
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
			// S4：流推送也标注 position_id（与拉路径同源）
			annotatePositionID(ctx, s.store, v.Positions)
			// F3：流推送也标注账号别名（与拉路径同源）
			annotateAccountAlias(ctx, s.store, v.Positions)
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
// 返回每策略的承诺年化（活跃持仓 expected_annual_pct 均值）vs 实际年化（realized_pnl 估算）。
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
		// 实际年化估算：realized_pnl / (notional × hold_days) × 365
		// notional 取配置默认值（单持仓名义额，估算口径）
		if a.AvgHoldDays > 0 && s.notionalUSD > 0 && a.TotalPositions > 0 {
			notionalTotal := s.notionalUSD * float64(a.TotalPositions)
			entry.ActualAnnualPct = a.RealizedPnlUsd / (notionalTotal * a.AvgHoldDays) * 365 * 100
		}
		entries = append(entries, entry)
	}
	return connect.NewResponse(&v1.GetStrategyLedgerResponse{Entries: entries}), nil
}
