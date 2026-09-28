// paper_fees.go D-474a S2：分腿真值费用表——venue 原生现行公开 taker 费率。
// 值以钉稿日 2026-09-25 venue 官方费率页为准（漂移属已知残差，表化热改另案）：
//
//	binance spot 10bps / perp 4.5bps（SSOT 钉值；BNB 折扣未计=保守高估）
//	okx     spot 10bps / perp 5bps（Regular 0.1000%/0.0500%，okx.com 费率表）
//	gate    spot 10bps / perp 5bps（VIP0 0.1%/0.05%，gate.com 费率页）
//	mexc    spot 5bps  / perp 2bps（0.05%/0.02%，mexc.com/fee）
package scan

// venueTakerBps venue→{spot taker, perp taker}（bps）。
var venueTakerBps = map[string]struct{ spot, perp float64 }{
	"binance": {10.0, 4.5},
	"okx":     {10.0, 5.0},
	"gate":    {10.0, 5.0},
	"mexc":    {5.0, 2.0},
}

// mtCommissionUSD MT 腿每手佣金常数：无常数实值 → 0（SSOT：不算缺陷，行 note 注明「佣金未计」）。
const mtCommissionUSD = 0.0

// paperSpotCarryFeeUSD spot_carry 四笔往返费：spot 进+出 + perp 开+平（venue 未覆盖→nil 拒填）。
func paperSpotCarryFeeUSD(venue string, qtyUSD float64) *float64 {
	f, ok := venueTakerBps[venue]
	if !ok {
		return nil
	}
	v := qtyUSD * (2*f.spot + 2*f.perp) / 1e4
	return &v
}

// paperPerpLegRoundTripUSD 单 perp 腿开平往返费（venue 未覆盖→nil）。
func paperPerpLegRoundTripUSD(venue string, qtyUSD float64) *float64 {
	f, ok := venueTakerBps[venue]
	if !ok {
		return nil
	}
	v := qtyUSD * 2 * f.perp / 1e4
	return &v
}

// PaperSpotPerpFeeUSD D-555 S2：双CEX spot×perp 四笔往返费（spot 进+出+perp 开+平；
// 任一 venue 未覆盖→nil 拒填）。导出与 PaperPerpPerpFeeUSD 同族（同表单源）。
func PaperSpotPerpFeeUSD(spotVenue, perpVenue string, qtyUSD float64) *float64 {
	fs, ok := venueTakerBps[spotVenue]
	if !ok {
		return nil
	}
	fp, ok2 := venueTakerBps[perpVenue]
	if !ok2 {
		return nil
	}
	v := qtyUSD * (2*fs.spot + 2*fp.perp) / 1e4
	return &v
}

// PaperPerpPerpFeeUSD perp×perp 四笔：两所各往返（任一 venue 未覆盖→nil 拒填）。
// （导出：tools/universe-radar 轨复用，D-474a S2）。
func PaperPerpPerpFeeUSD(shortVenue, longVenue string, qtyUSD float64) *float64 {
	a, b := paperPerpLegRoundTripUSD(shortVenue, qtyUSD), paperPerpLegRoundTripUSD(longVenue, qtyUSD)
	if a == nil || b == nil {
		return nil
	}
	v := *a + *b
	return &v
}

// paperCfdFeeUSD cfd×perp 费：cex 腿往返 2 笔 + MT 腿=monitor 簿实算点差往返（2×spread）
// + MT 每手佣金常数（0=未计）。cex venue 未覆盖或 monitor 簿缺 → nil（fail-closed 缺账）。
func paperCfdFeeUSD(cexVenue string, qtyUSD float64, spreadBps *float64) *float64 {
	cex := paperPerpLegRoundTripUSD(cexVenue, qtyUSD)
	if cex == nil || spreadBps == nil {
		return nil
	}
	v := *cex + 2*qtyUSD*(*spreadBps)/1e4 + mtCommissionUSD
	return &v
}
