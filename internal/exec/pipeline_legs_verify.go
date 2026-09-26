package exec

import (
	"arb/internal/keydomain"
	"context"
	"fmt"
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
	// 按 venue 分组待查腿（D-417：存腿而非仅 ticket——CEX 匹配要 Symbol/Side）
	venueLegs := map[string][]pgstore.PositionLeg{}
	for _, leg := range legs {
		if leg.Phase != "OPEN" || leg.Status != "FILLED" || leg.OrderID == "" {
			continue
		}
		venueLegs[leg.Venue] = append(venueLegs[leg.Venue], leg)
	}
	if len(venueLegs) == 0 {
		return nil, nil
	}

	var residual []string
	for venueKey, vlegs := range venueLegs {
		vctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		orders, err := p.verifier.QueryOpenedOrders(vctx, venueKey)
		cancel()
		if err != nil {
			// D-417：CEXPositionQuerier D-121 起在册——查询失败与 MT 同权 fail-closed
			// （旧豁免 continue=真实查询失败 fail-open 漏检残余）。
			return nil, fmt.Errorf("verify residual %s: %w", venueKey, err)
		}
		for _, leg := range vlegs {
			if LegAtVenue(leg, orders) {
				residual = append(residual, fmt.Sprintf("%s ticket=%s", venueKey, leg.OrderID))
			}
		}
	}
	return residual, nil
}

// LegAtVenue 判定本地腿在 venue opened-orders 里是否存活（D-417 统一匹配器）。
// MT 腿：Ticket==OrderID 精确匹配；CEX 腿：Symbol+Side(+PosSide 非空时)——
// CEX querier 的 Ticket=instId 与 leg.OrderID=ordId 永不命中（审计 P1-1 实证），
// 正确范式同 brokerPreflight（pipeline_resume_helpers.go）。
func LegAtVenue(leg pgstore.PositionLeg, orders []OpenedOrder) bool {
	for _, o := range orders {
		if isMTVenueKey(leg.Venue) {
			if o.Ticket == leg.OrderID {
				return true
			}
			continue
		}
		if o.Symbol != leg.Symbol || o.Side != leg.Side {
			continue
		}
		if o.PosSide == "" {
			return true // 旧适配器/MT 透传无 PosSide → 回退 Symbol+Side（D-195-C2 同语义）
		}
		want := "long"
		if leg.Side == "SELL" {
			want = "short"
		}
		if o.PosSide == want {
			return true
		}
	}
	return false
}

// isMTVenueKey 判断 venueKey 是否为 MT5/MT4 venue（D-120：CEX venue 平仓验证跳过用）。
func isMTVenueKey(venueKey string) bool {
	return keydomain.IsMTKey(venueKey)
}

// isCEXVenueKey D-195-C2：判断 venueKey 是否为 CEX（与 isMTVenueKey 对称）。
func isCEXVenueKey(venueKey string) bool {
	venue, _, _ := strings.Cut(venueKey, ":")
	return isCEXVenue(venue)
}

// isCEXVenue D-195-C2：判断 venue 是否为 CEX（okx/binance/gate）。
func isCEXVenue(venue string) bool {
	switch venue {
	case "okx", "binance", "gate":
		return true
	default:
		return false
	}
}
