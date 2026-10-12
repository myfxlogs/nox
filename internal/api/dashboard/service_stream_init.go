// service_stream_init.go —— D-508 S9 行数折分（自 service_stream.go 搬出，纯搬家零改动）。
package dashboard

import (
	"context"
	"fmt"

	"connectrpc.com/connect"

	v1 "arb/internal/gen/dashboardv1"
	"arb/internal/store/pgstore"
)

// streamInitQuotes D-090/D-508 S9 拆出：流启动初始化——缓存未初始化则先查库填缓存；首帧快照逐条 Send。
func (s *Service) streamInitQuotes(ctx context.Context, stream *connect.ServerStream[v1.StreamUpdatesResponse]) error {
	if err := s.ensureWatchlistLoaded(ctx); err != nil {
		return fmt.Errorf("stream init watchlist: %w", err)
	}
	for _, q := range s.qbus.AllLatest() {
		if !s.watchlistHitQuote(q) {
			continue
		}
		if err := stream.Send(&v1.StreamUpdatesResponse{
			Update: &v1.StreamUpdatesResponse_Quote{Quote: quoteToProto(q)},
		}); err != nil {
			return err
		}
	}
	return nil
}

// streamInitBrokerPositions D-571 包A：Broker 实持仓回放——cur 全量每仓一条 case 15 upsert
// 逐条 Send（同 streamInitQuotes 模式；新连接秒级全量，不依赖事件到达）。
func (s *Service) streamInitBrokerPositions(stream *connect.ServerStream[v1.StreamUpdatesResponse]) error {
	if s.bpm == nil {
		return nil
	}
	for _, ev := range s.bpm.Events() {
		if err := stream.Send(&v1.StreamUpdatesResponse{
			Update: &v1.StreamUpdatesResponse_BrokerPositionEvent{BrokerPositionEvent: ev},
		}); err != nil {
			return err
		}
	}
	return nil
}

// streamInitPositionsAndAccounts D-571：持仓+账户回放合并入口（包A case15/包B case16/包C 四域）。
func (s *Service) streamInitPositionsAndAccounts(ctx context.Context, stream *connect.ServerStream[v1.StreamUpdatesResponse]) error {
	if err := s.streamInitBrokerPositions(stream); err != nil {
		return err
	}
	if err := s.streamInitAccounts(stream); err != nil {
		return err
	}
	if err := s.streamInitPosMonDomains(ctx, stream); err != nil {
		return err
	}
	return s.streamInitOppsAlerts(ctx, stream)
}

// updateSender 抽象 send 端（*connect.ServerStream 满足；测试可注录制 fake）。
type updateSender interface {
	Send(*v1.StreamUpdatesResponse) error
}

// streamInitOppsAlerts D-571 包C：机会/告警回放——前端 load() 删除后，case2/case3
// 载荷帧是水合唯一源（同逐条 Send 模式；opp 组装走 oppToProto 与 ListOpportunities 同路径）。
// D-138：逆序回放——ListOpportunities(net_bps DESC)/ListAlerts(ts DESC) 送前端 upsertById
// 头插语义，正序送会反成 ASC（最新/最优垫底）；逆序送=ASC 入→头插落回 DESC。
// 核查项结论：positions/sourceHealth/accounts/brokerPositions 均 keyed upsert（同 key 替换
// 非 prepend）+positions 源是 latest-map 无序遍历，展示侧无顺序依赖——仅本两域需修。
func (s *Service) streamInitOppsAlerts(ctx context.Context, stream *connect.ServerStream[v1.StreamUpdatesResponse]) error {
	opps, err := s.store.ListOpportunities(ctx, "", 200, 0)
	if err != nil {
		return fmt.Errorf("stream init opportunities: %w", err)
	}
	if err := s.sendReplayOpps(ctx, stream, opps); err != nil {
		return err
	}
	alerts, err := s.store.ListAlerts(ctx, 200, 0)
	if err != nil {
		return fmt.Errorf("stream init alerts: %w", err)
	}
	return s.sendReplayAlerts(stream, alerts)
}

// sendReplayOpps 逆序回放机会帧（D-138：ASC 送→头插落回 net_bps DESC）。
func (s *Service) sendReplayOpps(ctx context.Context, stream updateSender, opps []pgstore.Opportunity) error {
	for i := len(opps) - 1; i >= 0; i-- {
		if err := stream.Send(&v1.StreamUpdatesResponse{
			Update: &v1.StreamUpdatesResponse_Opportunity{Opportunity: s.oppToProto(ctx, &opps[i])},
		}); err != nil {
			return err
		}
	}
	return nil
}

// sendReplayAlerts 逆序回放告警帧（D-138：ASC 送→头插落回 ts DESC）。
func (s *Service) sendReplayAlerts(stream updateSender, alerts []pgstore.Alert) error {
	for i := len(alerts) - 1; i >= 0; i-- {
		if err := stream.Send(&v1.StreamUpdatesResponse{
			Update: &v1.StreamUpdatesResponse_Alert{Alert: alertToProto(&alerts[i])},
		}); err != nil {
			return err
		}
	}
	return nil
}

// streamInitPosMonDomains D-571 包C：posMon 域回放——positions(case4)/（D-063：personal case18 拆）/
// hedge(case17)/health(case5) latest 全量逐条/单帧 Send（首次访问触发 posMon boot 组装）。
func (s *Service) streamInitPosMonDomains(ctx context.Context, stream *connect.ServerStream[v1.StreamUpdatesResponse]) error {
	if s.posMon == nil {
		return nil
	}
	for _, p := range s.posMon.PositionEvents(ctx) {
		if err := stream.Send(&v1.StreamUpdatesResponse{
			Update: &v1.StreamUpdatesResponse_Position{Position: p},
		}); err != nil {
			return err
		}
	}
	for _, h := range s.posMon.HealthEvents(ctx) {
		if err := stream.Send(&v1.StreamUpdatesResponse{
			Update: &v1.StreamUpdatesResponse_SourceHealth{SourceHealth: h},
		}); err != nil {
			return err
		}
	}
	return nil
}

// streamInitAccounts D-571 包B：账户资产回放——cur 全量每 venue 一条 case 16 upsert
// 逐条 Send（同 streamInitBrokerPositions 模式；新连接秒级全量，不依赖事件到达）。
func (s *Service) streamInitAccounts(stream *connect.ServerStream[v1.StreamUpdatesResponse]) error {
	if s.acctMon == nil {
		return nil
	}
	for _, ev := range s.acctMon.Events() {
		if err := stream.Send(&v1.StreamUpdatesResponse{
			Update: &v1.StreamUpdatesResponse_AccountEvent{AccountEvent: ev},
		}); err != nil {
			return err
		}
	}
	return nil
}
