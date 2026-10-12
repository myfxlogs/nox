// paper_fees.go D-474a S2 + D-014 S3：分腿真值费用——源收编 exchange_configs.taker_fee_bps
// （venue 官方费率页播种值，业主可热改；econ 同一费率源，venue/venue_type 无行或 NULL
// → nil/false fail-closed 拒填，静态费率表退役）。D-474a 钉稿值见迁移 0169 播种注释。
package scan

import (
	"context"
)

// takerFeeSource exchange_configs.taker_fee_bps 读口（*pgstore.Store 原生实现；
// D-014：paper 两轨与 econ 共用同一费率源）。
type takerFeeSource interface {
	ExchangeTakerFeeBps(ctx context.Context, venue, venueType string) (float64, bool, error)
}

// mtCommissionUSD MT 腿每手佣金常数：无常数实值 → 0（SSOT：不算缺陷，行 note 注明「佣金未计」）。
const mtCommissionUSD = 0.0

// takerBps 腿费率取数（venue 裸名+通道型 spot/perp/funding）；缺源/查询失败 → false。
func takerBps(ctx context.Context, st takerFeeSource, venue, venueType string) (float64, bool) {
	if st == nil {
		return 0, false
	}
	v, ok, err := st.ExchangeTakerFeeBps(ctx, venue, venueType)
	if err != nil || !ok {
		return 0, false
	}
	return v, true
}

// paperSpotCarryFeeUSD spot_carry 四笔往返费：spot 进+出 + perp 开+平（费率缺→nil 拒填）。
func paperSpotCarryFeeUSD(ctx context.Context, st takerFeeSource, venue string, qtyUSD float64) *float64 {
	spot, ok1 := takerBps(ctx, st, venue, "spot")
	perp, ok2 := takerBps(ctx, st, venue, "perp")
	if !ok1 || !ok2 {
		return nil
	}
	v := qtyUSD * (2*spot + 2*perp) / 1e4
	return &v
}

// paperPerpLegRoundTripUSD 单 perp 腿开平往返费（费率缺→nil）。
func paperPerpLegRoundTripUSD(ctx context.Context, st takerFeeSource, venue string, qtyUSD float64) *float64 {
	perp, ok := takerBps(ctx, st, venue, "perp")
	if !ok {
		return nil
	}
	v := qtyUSD * 2 * perp / 1e4
	return &v
}

// PaperSpotPerpFeeUSD D-555 S2：双CEX spot×perp 四笔往返费（spot 进+出+perp 开+平；
// 任一费率缺→nil 拒填）。导出与 PaperPerpPerpFeeUSD 同族（同源同口径）。
func PaperSpotPerpFeeUSD(ctx context.Context, st takerFeeSource, spotVenue, perpVenue string, qtyUSD float64) *float64 {
	spot, ok1 := takerBps(ctx, st, spotVenue, "spot")
	perp, ok2 := takerBps(ctx, st, perpVenue, "perp")
	if !ok1 || !ok2 {
		return nil
	}
	v := qtyUSD * (2*spot + 2*perp) / 1e4
	return &v
}

// PaperPerpPerpFeeUSD perp×perp 四笔：两所各往返（任一费率缺→nil 拒填）。
// （导出：tools/universe-radar 轨复用，D-474a S2）。
func PaperPerpPerpFeeUSD(ctx context.Context, st takerFeeSource, shortVenue, longVenue string, qtyUSD float64) *float64 {
	a, b := paperPerpLegRoundTripUSD(ctx, st, shortVenue, qtyUSD), paperPerpLegRoundTripUSD(ctx, st, longVenue, qtyUSD)
	if a == nil || b == nil {
		return nil
	}
	v := *a + *b
	return &v
}

// paperCfdFeeUSD cfd×perp 费：cex 腿往返 2 笔 + MT 腿=monitor 簿实算点差往返（2×spread）
// + MT 每手佣金常数（0=未计）。费率缺或 monitor 簿缺 → nil（fail-closed 缺账）。
func paperCfdFeeUSD(ctx context.Context, st takerFeeSource, cexVenue string, qtyUSD float64, spreadBps *float64) *float64 {
	cex := paperPerpLegRoundTripUSD(ctx, st, cexVenue, qtyUSD)
	if cex == nil || spreadBps == nil {
		return nil
	}
	v := *cex + 2*qtyUSD*(*spreadBps)/1e4 + mtCommissionUSD
	return &v
}
