// service_venue_system_exposure.go D-009 S4→D-010：venue 保证金实占 USD——Σ 在途系统仓
// OPEN 腿 名义（qty×cs×现价×fx）× legMarginNeedFrac（D-563 链：MT=init_margin_rate fact
// /perp=杠杆实读/spot=1.0；frac 不可得→该腿按名义全款计=保守）。外单不入（不在 positions
// 表）。同窗兄弟仓在途无腿时按 positions.notional_usd×0.5 经 opportunity_id 解析
// typed venue_key（还原 venue 类型段取 frac，读不到→1.0）。
// 展示闸（闸6 exposureCapsGate）与执行闸（CreatePositionWithCapGuard venueSum 回调）
// 同源同判据共用本函数（P3）。行集查询=store.OpenVenueSystemLegs 单源。
package dashboard

import (
	"context"
	"fmt"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/notional"
	"arb/internal/store/pgstore"
)

// VenueSystemMarginUSD venue 保证金实占 USD（bare venue 名入参，pgstore 行查询同域）。
// 查询失败 → err（调用方 fail-closed）；单腿名义缺（现价/成交价/cs/fx 全断）→ 该腿计 0
// 并沿 frac=1 同名回退由 venueLegUSD 语义承托；frac 未知 → 名义全款计。
func (s *Service) VenueSystemMarginUSD(ctx context.Context, venueBare string) (float64, error) {
	rows, err := s.openVenueLegs(ctx, venueBare)
	if err != nil {
		return 0, err
	}
	canonOf := s.venueCanonicalMapper(ctx, venueBare)
	byPos := make(map[int64][]pgstore.OpenVenueLegRow)
	var order []int64
	for _, r := range rows {
		if _, ok := byPos[r.PositionID]; !ok {
			order = append(order, r.PositionID)
		}
		byPos[r.PositionID] = append(byPos[r.PositionID], r)
	}
	sum := 0.0
	now := time.Now()
	for _, id := range order {
		rs := byPos[id]
		legs := 0
		legSum := 0.0
		for _, r := range rs {
			if r.VenueKey == "" || r.Qty <= 0 {
				continue
			}
			legs++
			legSum += s.venueLegUSD(ctx, r, canonOf(r.Symbol), now) * s.legFracOrFull(ctx, r.VenueKey, r.Symbol, r.Side == "SELL")
		}
		if legs > 0 {
			sum += legSum
			continue
		}
		// 同窗兄弟仓在途（腿未落库 PosLegsTotal=0）：按 oppID 解析腿 venue，
		// 每命中一腿归属 notional×0.5（两腿组合惯例；同 venue 双腿可双命中）。
		// oppID=TYPE:canon:vB:vtB:vS:vtS —— 还原 typed key 取 frac（读不到→1.0 全款）。
		p := rs[0]
		if p.PosLegsTotal != 0 || p.PosNotional == nil {
			continue
		}
		half := *p.PosNotional / 2
		parts := strings.Split(p.PosOppID, ":")
		if len(parts) >= 6 {
			if parts[2] == venueBare {
				sum += half * s.legFracOrFull(ctx, parts[2]+":"+parts[3], "", false)
			}
			if parts[4] == venueBare {
				sum += half * s.legFracOrFull(ctx, parts[4]+":"+parts[5], "", true)
			}
		}
	}
	return sum, nil
}

// legFracOrFull 腿保证金率（占用口径）：legMarginNeedFrac 实读；unknown→1.0 名义全款计
// （D-010 边界：读败按全款=保守不放行，不猜杠杆）。
func (s *Service) legFracOrFull(ctx context.Context, venueKey, symbol string, sellLeg bool) float64 {
	if f, _, known := s.legMarginNeedFrac(ctx, venueKey, symbol, sellLeg); known && f > 0 {
		return f
	}
	return 1.0
}

// venueCanonicalMapper venue 的 symbol→canonical 解析闭包（MT=broker_symbol_map；
// CEX=CexSymbolKey 派生，与 protoFor 同径）。store/bm 缺 → CEX 派生兜底。
func (s *Service) venueCanonicalMapper(ctx context.Context, venueBare string) func(string) string {
	var m map[string]string
	if s.bm != nil {
		sctx, cancel := monitorCallCtx(ctx)
		m, _ = s.bm.BrokerSymbolMapWithCS(sctx, venueBare)
		cancel()
	}
	return func(sym string) string {
		if c := m[sym]; c != "" {
			return c
		}
		return bus.CexSymbolKey(sym) // CEX 无映射行时同 protoFor 派生
	}
}

// venueLegUSD 单腿 USD：qty×cs×价×fx——现价优先（qbus 腿级），缺回退成交价；
// cs=legContractSizeOf（canonical 单位口径，配 qbus canonical 价/ venue 原生价两宜：
// MT tick 为 canonical 价→canonical 口径 cs 正确；CEX 原生=canonical 同形）；
// fx=QuoteCcy→USDPer（USD 计价=1 免查）。
func (s *Service) venueLegUSD(ctx context.Context, r pgstore.OpenVenueLegRow, canonical string, now time.Time) float64 {
	px := 0.0
	if s.qbus != nil {
		if q, ok := s.qbus.LatestLeg(canonical, r.Symbol, r.VenueKey); ok {
			px = (q.Bid + q.Ask) / 2
		}
	}
	if px <= 0 && r.PriceActual != nil {
		px = *r.PriceActual
	}
	if px <= 0 {
		return 0
	}
	cs, err := s.legContractSizeOf(r.VenueKey, r.Symbol)
	if err != nil || cs <= 0 {
		return 0
	}
	fx := 1.0
	if q := notional.QuoteCcy(canonical); q != "" && q != "USD" {
		rate, ok := s.store.USDPer(ctx, q, now)
		if !ok || rate <= 0 {
			return 0
		}
		fx = rate
	}
	return r.Qty * cs * px * fx
}

// openVenueLegs 实占行集单源——openVenueLegsFn=测试 seam（fake 行注入）；
// 未接线且 store=nil → fail-closed err（实占不可算不许放行）。
func (s *Service) openVenueLegs(ctx context.Context, venueBare string) ([]pgstore.OpenVenueLegRow, error) {
	if s.openVenueLegsFn != nil {
		return s.openVenueLegsFn(ctx, venueBare)
	}
	if s.store == nil {
		return nil, fmt.Errorf("store 未接线")
	}
	return s.store.OpenVenueSystemLegs(ctx, venueBare)
}

// venueMarginSum 已内联：闸6/approve 闸直调 VenueSystemMarginUSD(ctx, BareOf(vk))（D-010）。
