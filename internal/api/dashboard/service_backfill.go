// service_backfill.go D-181 S1：BackfillSwapHistory RPC handler + venueKey 解析。
// 从 broker_order_history_scan.go 提取以保 ≤300 行（D-182 S3 同模式软超线拆分）。
package dashboard

import (
	"context"
	"fmt"
	"log"

	"nox/internal/data/keydomain"
	"time"

	v1 "nox/internal/gen/dashboardv1"

	"connectrpc.com/connect"
)

// BackfillSwapHistory D-181 S1 RPC handler：手动回溯 venue swap 历史→facts。
// 走认证拦截器（与 ExecuteOpportunity 同源）。
func (s *Service) BackfillSwapHistory(ctx context.Context, req *connect.Request[v1.BackfillSwapHistoryRequest]) (*connect.Response[v1.BackfillSwapHistoryReply], error) {
	venueKey := req.Msg.GetVenue()
	if venueKey == "" {
		return connect.NewResponse(&v1.BackfillSwapHistoryReply{
			Summary: "venue is required (e.g. 61585565:mt5)",
		}), nil
	}
	// 解析 venueKey → bare venue + platform
	bareVenue, platform, ok := splitVenueKey(venueKey)
	if !ok {
		return connect.NewResponse(&v1.BackfillSwapHistoryReply{
			Summary: fmt.Sprintf("invalid venueKey %q (expected venue:mt5（mt4 已退役）)", venueKey),
		}), nil
	}
	// 从 BrokerManager 获取 orderHistoryScan 闭包
	scan := s.bm.OrderHistoryScan(bareVenue)
	if scan == nil {
		return connect.NewResponse(&v1.BackfillSwapHistoryReply{
			Summary: fmt.Sprintf("venue %s not connected or no order history scanner (platform=%d)", bareVenue, platform),
		}), nil
	}
	// 拉 365 天历史
	to := time.Now().UTC()
	from := to.Add(-365 * 24 * time.Hour)
	orders, err := scan(ctx, from, to)
	if err != nil {
		return connect.NewResponse(&v1.BackfillSwapHistoryReply{
			Summary: fmt.Sprintf("scan failed: %v", err),
		}), nil
	}
	if len(orders) == 0 {
		return connect.NewResponse(&v1.BackfillSwapHistoryReply{
			Summary: "no closed orders in 365d, fallback to L2 probe",
		}), nil
	}
	// 过滤 Swap=0（无结算参考）
	var nonZero []HistorySwapOrder
	for _, o := range orders {
		if o.Swap != 0 && o.Lots > 0 {
			nonZero = append(nonZero, o)
		}
	}
	if len(nonZero) == 0 {
		return connect.NewResponse(&v1.BackfillSwapHistoryReply{
			OrdersScanned: int32(len(orders)),
			Summary:       "all orders Swap=0 (Deal-level swap?), fallback to L2 probe",
		}), nil
	}
	// 聚合中位数 → facts
	now := time.Now().UTC()
	facts := aggregateHistoryToUsdLotDay(nonZero, bareVenue, now)
	// 幂等：先删同 (kind, venue, src=order_history) 旧 fact 再写
	if err := s.store.DeleteFactsBySrc(ctx, "order_history", bareVenue); err != nil {
		log.Printf("[backfill-swap] delete old facts: %v", err)
	}
	if len(facts) > 0 {
		if err := s.store.InsertFacts(ctx, facts); err != nil {
			return connect.NewResponse(&v1.BackfillSwapHistoryReply{
				OrdersScanned: int32(len(orders)),
				Summary:       fmt.Sprintf("insert facts failed: %v", err),
			}), nil
		}
	}
	summary := fmt.Sprintf("scanned %d orders, %d with Swap≠0, wrote %d facts (src=order_history)",
		len(orders), len(nonZero), len(facts))
	return connect.NewResponse(&v1.BackfillSwapHistoryReply{
		OrdersScanned: int32(len(orders)),
		FactsWritten:  int32(len(facts)),
		Summary:       summary,
	}), nil
}

// splitVenueKey 解析 "venue:mt5" → (bareVenue, platform=1, ok)。
// mt4 已退役（资产面零户）——venue:mt4 按格式错误拒。
func splitVenueKey(venueKey string) (venue string, platform int, ok bool) {
	vk := keydomain.Parse(venueKey)
	if vk.Type == "" {
		return "", 0, false
	}
	switch vk.Type {
	case "mt5":
		return vk.Venue, 1, true
	}
	return "", 0, false
}
