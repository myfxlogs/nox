package exec

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

// checkResidualLegs 平仓后按 ticket 回读各 venue 在仓订单，返回残留腿描述（D-077 修复⑩ G1）。
// 只检查 FILLED 的 OPEN phase 腿（CLOSE phase 是刚写的平仓腿，不查）。
// D-108 A2 fail-closed：verifier 为 nil → 返回 error（调用方 ClosePosition 已检查 nil，
// 此处防御性返回 error 防直接调用 panic）；verifier 查询失败 → 返回 error（拒绝置 CLOSED）。
func (p *Pipeline) checkResidualLegs(ctx context.Context, legs []pgstore.PositionLeg) ([]string, error) {
	if p.verifier == nil {
		return nil, fmt.Errorf("verifier not wired")
	}
	// 按 venue 分组待查 ticket
	venueTickets := map[string][]string{} // venueKey → []ticket
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" || leg.OrderID == "" {
			continue
		}
		venueTickets[leg.Venue] = append(venueTickets[leg.Venue], leg.OrderID)
	}
	if len(venueTickets) == 0 {
		return nil, nil
	}

	var residual []string
	for venueKey, tickets := range venueTickets {
		vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		orders, err := p.verifier.QueryOpenedOrders(vctx, venueKey)
		cancel()
		if err != nil {
			// D-120：CEX venue 无 verifier querier（CEX 交易所订单状态权威，
			// ClosePosition 流程已确认平仓 FILLED=已平仓）。MT venue 仍 fail-closed。
			if !isMTVenueKey(venueKey) {
				log.Printf("[exec] verify residual: skip CEX venue %s (no querier, close FILLED=authoritative): %v", venueKey, err)
				continue
			}
			return nil, fmt.Errorf("verify residual %s: %w", venueKey, err)
		}
		// 构建 ticket → 在仓 集合
		opened := make(map[string]bool, len(orders))
		for _, o := range orders {
			opened[o.Ticket] = true
		}
		for _, ticket := range tickets {
			if opened[ticket] {
				residual = append(residual, fmt.Sprintf("%s ticket=%s", venueKey, ticket))
			}
		}
	}
	return residual, nil
}

// isMTVenueKey 判断 venueKey 是否为 MT5/MT4 venue（D-120：CEX venue 平仓验证跳过用）。
func isMTVenueKey(venueKey string) bool {
	return strings.HasSuffix(venueKey, ":mt5") || strings.HasSuffix(venueKey, ":mt4")
}
