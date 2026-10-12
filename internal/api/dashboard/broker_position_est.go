// broker_position_est.go D-009 S3：平仓净估列（close_now_estimate_usd）。
// est = (平仓方向价−开价)×qty×cs×fx − 平仓名义×takerBps×1e-4 + Σ已收息（D-037）。
// 方向价=bp.CurrentPrice（S2 起已是平仓方向价：BUY仓=bid/SELL仓=ask，缺双值回退帧中价）。
// cs/fx=USDPerLotForLeg 同族换算链（原生 cs 配原生价，fx=QuoteCcy→USDPer）；费率=
// commissionResolver（MT=broker_accounts.commission_bps→calib；CEX=cex_taker_fee_bps→xasset 回退）。
// D-167：Fresh 不入门——净估本就是估，用最可得价（stale 帧携/持久末帧）照算；价在=可估。
// 现价/开价/量为零 / canonical 缺 / cs·fx·费率任一不可得 → nil 诚实空态（列标「估」不冒充 venue 字段）。
package dashboard

import (
	"context"

	"time"

	"nox/internal/data/keydomain"
	v1 "nox/internal/gen/dashboardv1"
	"nox/internal/platform/notional"
)

// annotateCloseEstimate D-009 S3：逐 broker 持仓行算平仓净估（拉路径与事件路径同挂）。
// 单腿任一输入缺 → 该行 nil（不影响他行）；无 qbus/store/resolver → 整体跳过。
func (s *Service) annotateCloseEstimate(ctx context.Context, bps []*v1.BrokerPosition) {
	for _, bp := range bps {
		if est, ok := s.closeEstimateUSD(ctx, bp); ok {
			bp.CloseNowEstimateUsd = &est
		}
	}
}

// closeEstimateUSD 单行净估。返回 ok=false = 任一输入不可得（诚实缺省）。
func (s *Service) closeEstimateUSD(ctx context.Context, bp *v1.BrokerPosition) (float64, bool) {
	// D-167：!bp.Fresh 不拒——stale 价（休市帧携/ticks 末帧兜底）同样可估，「-」反而是假无数据。
	if bp == nil || bp.CurrentPrice <= 0 || bp.OpenPrice <= 0 || bp.Volume <= 0 {
		return 0, false
	}
	// cs：CEX 走 legContractSizeOf（perp=ctVal/spot=1）；MT 走原生 legContractSize
	// （价是帧携/venue 原生价，勿配 canonical 单位口径 cs——legContractSizeOf 的
	// qty_scale 是 canonical 价专用，混配属 D-594 禁例）。
	var cs float64
	if isCEXVenueKey(bp.VenueKey) {
		c, err := s.legContractSizeOf(bp.VenueKey, bp.Symbol)
		if err != nil || c <= 0 {
			return 0, false
		}
		cs = c
	} else {
		if s.legContractSize == nil {
			return 0, false
		}
		c, ok := s.legContractSize(bp.VenueKey, bp.Symbol)
		if !ok || c <= 0 {
			return 0, false
		}
		cs = c
	}
	// fx：计价币→USD（canonical 缺=计价币不可推→缺省）。USD 计价 fx=1 免查库。
	fx := 1.0
	if q := notional.QuoteCcy(bp.Canonical); q != "" && q != "USD" {
		if s.store == nil {
			return 0, false
		}
		rate, ok := s.store.USDPer(ctx, q, time.Now())
		if !ok || rate <= 0 {
			return 0, false
		}
		fx = rate
	}
	// takerBps：commissionResolver 链；CEX 缺时回退 xasset 费率（D-480 同表）；
	// 双缺→费率按 0 估（平仓费缺省≠阻塞——浮动才是主项，与 D-480 口径一致）。
	bps := s.VenueCommissionBps(bp.VenueKey)
	if bps <= 0 && keydomain.IsType(bp.VenueKey, "perp") {
		bps = s.xassetCexFeeBps
	}
	gross := (bp.CurrentPrice - bp.OpenPrice) * bp.Volume * cs * fx
	if bp.Side == "SELL" {
		gross = -gross
	}
	notionalUSD := bp.Volume * cs * bp.CurrentPrice * fx
	// D-037：净估加 Σ已收息——源不可得 → 不含息 + close_estimate_excludes_income 打标
	// （估值仍出，诚实标缺息而非空态：价差−费仍是有效主项）。
	income, excluded := s.receivedIncome(ctx, bp)
	if excluded {
		bp.CloseEstimateExcludesIncome = true
	}
	return gross - notionalUSD*bps/10000 + income, true
}

// receivedIncome D-037：Σ已收息（持仓已落账的 funding/swap）分源取——
// MT 腿=帧携 Order.Swap 原生（protoFor 已透传，venue 权威已计提值）；
// CEX 系统仓（position_id>0）=D-013 position_cashflows funding/swap actual 合计；
// CEX 外单=SumIncomeWindow(openedAt→now)（venue 粒度 income_events，同 annotateCEXSwap 口径）。
// excluded=true=income 源不可得（openedAt 缺/查询失败/store 未接线）→净估不含息。
func (s *Service) receivedIncome(ctx context.Context, bp *v1.BrokerPosition) (float64, bool) {
	if !isCEXVenueKey(bp.VenueKey) {
		return bp.Swap, false
	}
	if s.store == nil {
		return 0, true
	}
	if bp.PositionId > 0 {
		sum, err := s.store.SumPositionCarryActual(ctx, bp.PositionId)
		if err != nil {
			return 0, true
		}
		return sum, false
	}
	if bp.OpenedAt == nil {
		return 0, true // openedAt 缺→收入窗无界会错价，不含息打标（票意）
	}
	sum, _, err := s.store.SumIncomeWindow(ctx, bp.VenueKey, bp.Symbol, bp.OpenedAt.AsTime(), time.Now())
	if err != nil {
		return 0, true
	}
	return sum, false
}
