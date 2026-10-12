// engine_opp_qty.go —— opp 处理数量/名义额纯助手（自 engine_opp.go 整搬，纯搬家零语义——保 ≤300 行门禁）。
package autotrade

import (
	"log"
	"time"

	"arb/internal/store/pgstore"
	"arb/internal/trading/exec"
)

// effectiveNotionalUSD D-367 S4：有效名义额=组合级 spec 列优先，NULL（行缺列或 env 来源）
// 回退全局 env——渐进收编零漂移。纯函数（S5 T1/T2 锚点）。
func effectiveNotionalUSD(cfgNotional float64, spec *exec.XAssetComboAllowSpec) float64 {
	if spec != nil && spec.NotionalUSD != nil {
		return *spec.NotionalUSD
	}
	return cfgNotional
}

// computeBuyQty 计算买腿 qty（D-190：USD notional 统一，价格源改 live qbus ask）。
// usdPerLot = e.service.USDPerLotForLeg（内部 legContractSizeOf + usdnotional.PerLot，CS 源单一）。
// ok=false → skip=true（fail-closed log 含 opp.ID 与原因：CS-miss / no-quote / fx-miss）。
// **行为变化（D-190 有意）**：价格源从 opp.PriceBuy（机会行，可能陈旧）改为 live qbus ask（更优口径）；
// CEX 腿现在也要求 qbus 报价在（detector 产 opp 前提下必有）。
// 返回值扩 perLot（H4.2 spec 可行量下限换算 intent.NotionalUSD=qty×perLot 实额，取最小 diff）。
func computeBuyQty(opp pgstore.Opportunity, notionalUSD float64, usdPerLot func(canonical, venueKey, side string) (float64, bool)) (qty, perLot float64, skip bool) {
	perLot, ok := usdPerLot(opp.Canonical, opp.VenueBuy, "ask")
	if !ok || perLot <= 0 {
		log.Printf("[autotrade] skip opp=%s: USD-per-lot unavailable (fail-closed) canonical=%s venue=%s", opp.ID, opp.Canonical, opp.VenueBuy)
		return 0, 0, true
	}
	return notionalUSD / perLot, perLot, false
}

// xassetEntryGateMaxAge D-335：monitor 行龄上限（5min 采集节奏的 6 倍容差）；过期 fail-open。
const xassetEntryGateMaxAge = 30 * time.Minute

// xassetQtyFloor D-330 断层②：XASSET_CARRY qty<minStep 时的 spec 驱动可行量下限（§2.2）。
// MaxLongQty 复用为「spec 核准可行量」（cap 字段兼任——floorToStepQtyMin 无私有外出路，
// QtyBelowMinStep(bool) 是现成公共判据；误配大值由 gross 预闸 + pipeline checkXAssetComboAllow
// 终审双层兜底，安全方向正确）。gross 预闸=双腿 2×qty×perLot ≤ MaxGrossUSD（>0 时）。
// ok=false → 调用方维持静默跳过（原 H4.2 语义）；正常路径（qty≥minStep）不消费 MaxLongQty。
func xassetQtyFloor(spec *exec.XAssetComboAllowSpec, specQtyExecutable bool, perLot float64) (qty, notionalUSD float64, ok bool) {
	if spec == nil || spec.MaxLongQty <= 0 || !specQtyExecutable {
		return 0, 0, false
	}
	if spec.MaxGrossUSD > 0 && 2*spec.MaxLongQty*perLot > spec.MaxGrossUSD {
		return 0, 0, false
	}
	return spec.MaxLongQty, spec.MaxLongQty * perLot, true
}
