package dashboard

import (
	"context"
	"log"
	"time"

	"arb/internal/bus"
	"arb/internal/dephealth"
	"arb/internal/safego"

	v1 "arb/internal/gen/dashboardv1"

	"arb/internal/collect/exchange"
	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

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
		pbOpps = append(pbOpps, s.oppToProto(ctx, &o))
	}
	return connect.NewResponse(&v1.ListOpportunitiesResponse{Opportunities: pbOpps}), nil
}

func (s *Service) ListAlerts(ctx context.Context, req *connect.Request[v1.ListAlertsRequest]) (*connect.Response[v1.ListAlertsResponse], error) {
	// P5 修复⑧：告警 TTL 清理（按日节流，幂等；删除 acked+超 7 天，未 acked 不删）。
	s.maybePurgeAckedAlerts(ctx)
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

// maybePurgeAckedAlerts 内存节流（按日一次）触发告警 TTL 清理（P5 修复⑧）。
// 幂等：超 24h 或从未执行过 → 调 PurgeAckedAlerts 删除 acked+超 7 天行；失败静默（不阻断 ListAlerts）。
func (s *Service) maybePurgeAckedAlerts(ctx context.Context) {
	if time.Since(s.lastAlertPurge) < 24*time.Hour {
		return
	}
	s.lastAlertPurge = time.Now()
	safego.GoLoop("service_snapshots:purge-acked", func() {
		n, err := s.store.PurgeAckedAlerts(context.Background(), 7*24*time.Hour)
		if err != nil {
			log.Printf("[alerts] purge acked failed: %v", err)
			return
		}
		if n > 0 {
			log.Printf("[alerts] purged %d acked alerts older than 7d", n)
		}
	})
}

func (s *Service) ListSourceHealth(ctx context.Context, req *connect.Request[v1.ListSourceHealthRequest]) (*connect.Response[v1.ListSourceHealthResponse], error) {
	return connect.NewResponse(&v1.ListSourceHealthResponse{Sources: s.assembleSourceHealth()}), nil
}

// assembleSourceHealth D-571 包C：ListSourceHealth 组装公共口（RPC 与 positionMon
// case5 发射器同源复用——禁第二套组装）。
func (s *Service) assembleSourceHealth() []*v1.SourceHealth {
	var out []*v1.SourceHealth
	// D-512 T4：WS 重连计量透出（SnapshotReconnects 只读聚合，前端只渲染不算）。
	for _, snap := range exchange.SnapshotReconnects() {
		row := &v1.SourceHealth{
			Venue:          snap.URL,
			State:          "ws",
			Source:         snap.URL,
			ReconnectCount: snap.TotalReconns,
			LastEventUnix:  0,
		}
		if !snap.LastWarn.IsZero() {
			row.LastEventUnix = snap.LastWarn.Unix()
		}
		out = append(out, row)
	}
	// D-571C-R2 F-1：venue 流在线态行（StreamStateSink 事件→bpMon.streamOnline——
	// 不编入则状态翻转踢 posMon 无 diff=死踢；键域 venue_online: 与 ws url 行不撞）。
	if s.streamOnlineFn != nil {
		for vk, on := range s.streamOnlineFn() {
			state := "offline"
			if on {
				state = "online"
			}
			out = append(out, &v1.SourceHealth{
				Source: "venue_online:" + vk,
				Venue:  vk,
				State:  state,
			})
		}
	}
	// D-579 S5：上游依赖健康面并入透出（被动记账注册表；Source=dep 键与 ws
	// 重连行键域不撞；ReconnectCount 恒 0——dephealth 管断/通态不管抖动）。
	// merge 裁决：编入共享组装而非 RPC 体——case5 发射器与 ListSourceHealth 同源。
	for _, dep := range dephealth.Snapshot() {
		out = append(out, &v1.SourceHealth{
			Source:        dep.Key,
			State:         dep.State,
			LastEventUnix: dep.LastEventUnix,
		})
	}
	return out
}

// SetStreamOnlineSource D-571C-R2 F-1：流在线态读源注入（main 接 bpMon.StreamOnlineSnapshot；nil=缺该域行）。
func (s *Service) SetStreamOnlineSource(fn func() map[string]bool) { s.streamOnlineFn = fn }

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

// --- 告警删除 ---

func (s *Service) DeleteAlerts(ctx context.Context, req *connect.Request[v1.DeleteAlertsRequest]) (*connect.Response[v1.DeleteAlertsResponse], error) {
	n, err := s.store.DeleteAlerts(ctx, req.Msg.GetIds())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.DeleteAlertsResponse{Deleted: int32(n)}), nil
}

func (s *Service) ClearAckedAlerts(ctx context.Context, req *connect.Request[v1.ClearAckedAlertsRequest]) (*connect.Response[v1.ClearAckedAlertsResponse], error) {
	n, err := s.store.ClearAckedAlerts(ctx)
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	return connect.NewResponse(&v1.ClearAckedAlertsResponse{Deleted: int32(n)}), nil
}
