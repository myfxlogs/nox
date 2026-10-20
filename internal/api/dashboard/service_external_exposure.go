// service_external_exposure.go —— D-511 外单敞口并表（中间态）：
// external=台账查无记录（position_id=0）的 broker 仓——真实账户 delta，敞口透视并入
// 分子但生命周期零接管（不开不平不追踪不冷却）。名义口径镜像 venueLegUSD：
// |qty×cs×价×fx|；价/规格/fx 任一缺→该行剔计不编（诚实缺额，行仍在持仓面显示）。
package dashboard

import (
	"context"
	"strings"
	"time"

	"nox/internal/data/bus"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/notional"

	"connectrpc.com/connect"
)

// CloseBrokerPosition 经注册执行器平仓一条 venue 持仓（人工确认后，§1）。
// D-511：外单守卫先行——external 仓 FAILED 拒平（生命周期零接管）。
func (s *Service) CloseBrokerPosition(ctx context.Context, req *connect.Request[v1.CloseBrokerPositionRequest]) (*connect.Response[v1.CloseBrokerPositionResponse], error) {
	if estr := s.externalCloseGuard(req.Msg.GetVenueKey(), req.Msg.GetTicket()); estr != "" {
		return connect.NewResponse(&v1.CloseBrokerPositionResponse{Status: "FAILED", Message: estr}), nil
	}
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
		if estr := s.externalCloseGuard(it.GetVenueKey(), it.GetTicket()); estr != "" {
			results = append(results, &v1.CloseBrokerPositionResponse{Status: "FAILED", Message: estr})
			continue
		}
		if _, err := s.bm.CloseOrder(ctx, it.GetVenueKey(), it.GetTicket()); err != nil {
			results = append(results, &v1.CloseBrokerPositionResponse{Status: "FAILED", Message: err.Error()})
			continue
		}
		results = append(results, &v1.CloseBrokerPositionResponse{Status: "CLOSED"})
	}
	return connect.NewResponse(&v1.CloseBrokerPositionsResponse{Results: results}), nil
}

// externalCloseGuard 平仓执行径外单守卫：快照可识别为 external → 拒（生命周期零接管）；
// 快照无该行/bpm 未接线 → 放行既有径（关单 fail-open：拦不住的仓比拦错的仓安全）。
func (s *Service) externalCloseGuard(venueKey, ticket string) string {
	if s.bpm == nil {
		return ""
	}
	for _, bp := range s.bpm.Snapshot() {
		if bp.VenueKey == venueKey && bp.Ticket == ticket {
			if bp.External {
				return "外单不接管（external 仓系统不开不平——请 venue 侧直接操作）"
			}
			return ""
		}
	}
	return ""
}

// externalNotionalUSD venue 外单名义 USD 合计（bare venue 入参；bpm 快照已标注
// External）。价取 qbus.LatestLeg（canonical→原生键兜底同持仓径），cs 走
// legContractSizeOf，fx 走 USDPer——与 VenueSystemMarginUSD 系统腿同口径。
// bpm 未接线/快照空 → 0；单行缺价缺规格 → 剔计（不编值）。
func (s *Service) externalNotionalUSD(ctx context.Context, venueBare string, now time.Time) float64 {
	if s.bpm == nil {
		return 0
	}
	sum := 0.0
	for _, bp := range s.bpm.Snapshot() {
		if !bp.External || strings.SplitN(bp.VenueKey, ":", 2)[0] != venueBare || bp.Volume <= 0 {
			continue
		}
		canonical := bp.Canonical
		if canonical == "" && bus.IsCexVenueKey(bp.VenueKey) {
			canonical = bus.CexSymbolKey(bp.Symbol)
		}
		px := 0.0
		if s.qbus != nil {
			if q, ok := s.qbus.LatestLeg(canonical, bp.Symbol, bp.VenueKey); ok && q.Bid > 0 && q.Ask > 0 {
				px = (q.Bid + q.Ask) / 2
			}
		}
		if px <= 0 {
			px = bp.CurrentPrice // 报价断→回退行内现价（0=剔计不编）
		}
		if px <= 0 {
			continue
		}
		cs, err := s.legContractSizeOf(bp.VenueKey, bp.Symbol)
		if err != nil || cs <= 0 {
			continue
		}
		fx := 1.0
		if q := notional.QuoteCcy(canonical); q != "" && q != "USD" {
			rate, _, ok := s.usdPerOrStale(ctx, q, bp.VenueKey, now)
			if !ok || rate <= 0 {
				continue
			}
			fx = rate
		}
		sum += bp.Volume * cs * px * fx
	}
	return sum
}
