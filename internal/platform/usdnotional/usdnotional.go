// Package usdnotional 提供 USD/手 名义额换算的单一真相源（D-190 修复 A）。
//
// 修复前双拷贝：dashboard.usdPerLot + autotrade.notionalPerLotUSD（同公式同白名单）。
// 修复后统一为本包 PerLot，行为零变化（公式/白名单/fx 查找逐字迁移）。
//
// 依赖仅 internal/bus（叶子包，无环）。
package usdnotional

import (
	"strings"

	"nox/internal/data/bus"
)

// usdQuotedCommodities USD 计价商品/指数 CFD 白名单（无 USD 后缀但 USD 计价）。
// 显式列举避免 default 分支误拒（D-166：BRENT CMC×PXBT 净 swap +5.67 bps/日被误拒实证）。
// 从 dashboard.usdPerLot + autotrade.notionalPerLotUSD 双源逐字合并（两源内容一致）。
var usdQuotedCommodities = map[string]bool{
	"BRENT": true, "WTI": true, "SILVER": true, "COPPER": true, "NATGAS": true,
	"XAUUSD": true, "XAGUSD": true, "XPDUSD": true, "XPTUSD": true, "XCUUSD": true, // D-594：铜 canonical
	"NAS100": true, "SPX500": true, "US30": true, "US2000": true, "GER40": true,
	"UK100": true, "HK50": true, "JP225": true, "AUS200": true, "ES35": true,
	"FR40": true, "STOXX50": true,
}

// PerLot 返回 1 手的 USD 名义额。ok=false = fail-closed（勿猜）。
//
// 报价计价币由 canonical 后缀判定：
//   - USD 计价（canonical 以 USDT/USD 结尾，或商品/指数 CFD 白名单）→ CS × price（直接美元名义）；
//   - JPY 计价（canonical 以 JPY 结尾）→ CS × price ÷ USDJPY（USDJPY 取 qbus 最新任一 venue mid）；
//   - 其他计价币（EURGBP 等）→ false（未支持，fail-closed）。
//
// price 为该腿报价（bid/ask 由调用方决定）；qbus 用于查 fx 对（USDJPY 等）。
// contractSize<=0 或 price<=0 → false；qbus nil → false（JPY 腿）。
// contractSize 形参口径=canonical 单位数/手（MT 腿=contract_size×qty_scale，
// 由 legContractSizeOf/UnitQty 链折叠供入）；price 须同 canonical 口径（发射口已乘
// price_scale）。配原生成交价（PriceActual）的消费方传原生 CS——同口径配对即正确，
// qty_scale 不得乘（原生价×原生 CS 已是原生名义额）。
func PerLot(canonical string, price, contractSize float64, qbus *bus.QuoteBus) (float64, bool) {
	if contractSize <= 0 || price <= 0 {
		return 0, false
	}
	switch {
	case strings.HasSuffix(canonical, "USDT"), strings.HasSuffix(canonical, "USD"), usdQuotedCommodities[canonical]:
		// USD 计价：CS × price 直接美元名义（含商品/指数 CFD 白名单）
		return contractSize * price, true
	case strings.HasSuffix(canonical, "JPY"):
		// JPY 计价：CS × price ÷ USDJPY（日元名义换算美元）
		fx, ok := latestAnyVenue(qbus, "USDJPY")
		if !ok || fx <= 0 {
			return 0, false
		}
		return contractSize * price / fx, true
	default:
		// 其他计价币未支持 → fail-closed（勿猜）
		return 0, false
	}
}

// latestAnyVenue 从 qbus 取指定 canonical 的最新任一 venue 报价（mid 价）。
// 用于 fx 对换算（USDJPY 等，不限定 venue）。
func latestAnyVenue(qbus *bus.QuoteBus, canonical string) (float64, bool) {
	if qbus == nil {
		return 0, false
	}
	var best bus.Quote
	found := false
	for _, q := range qbus.AllLatest() {
		if q.Symbol == canonical {
			if !found || q.Time.After(best.Time) {
				best = q
				found = true
			}
		}
	}
	if !found {
		return 0, false
	}
	return (best.Bid + best.Ask) / 2, true
}
