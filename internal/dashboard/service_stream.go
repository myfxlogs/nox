package dashboard

import (
	"context"
	"fmt"
	"time"

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
			// S4：流推送也标注 position_id（与拉路径同源）
			annotatePositionID(ctx, s.store, v.Positions)
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
