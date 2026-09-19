// mt_swap_mode.go MT swap 模式映射与单日 bps 归一——自 mt_swap.go 拆出（D-309 纯移动，零行为变更）。
package adapter

import (
	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// SwapMode 语义化 swap 模式（MT4/MT5 原始值映射到此枚举后统一换算）。
type SwapMode int

const (
	SwapModeNone           SwapMode = iota // 真停用：发 0 值 Fact
	SwapModeInPoints                       // 点数：S × Points / price
	SwapModePerLotCurrency                 // 每手货币额：S / (price × ContractSize)（mode 2/3/4）
	SwapModeAnnualPct                      // 年化百分比：S% / 365（mode 5/6）
	SwapModeUncalibrated                   // 未标定 / 矛盾数据：跳过 + 计数日志（mode 7/8/10+）
)

// swapFreeWhitelist swap-free 账单豁免白名单（server→exempt symbol 集合，D-342R1 修订
// D-300 S0 的 server 前缀级判定）。语义：集合内 "*"=该 server 全品种免（real 账户账单级
// 豁免，D-300 S0 实证：3001157 XAUUSDm 名义 -534.9 实收 0）；具名 symbol=仅该形免。
// D-342R1 收窄实证：Exness-MT5Trial5(277842155) 账单执行 swap（pos 492 XAUUSD247m 实收
// -0.55/日，2026-09-18 rollover）——D-300「Exness 账单不执行」对 Trial 账户不成立，仅
// 标准形 XAUUSD 保留免；m/247m 变体走正常值路径（raw bps/day 入 canonical→ledger 入账）。
// 新增条目须业主确认账单层豁免后追加；删改走 D# 留痕。
var swapFreeWhitelist = map[string]map[string]bool{
	"Exness-MT5Real":   {"*": true},
	"Exness-MT5Real5":  {"*": true},
	"Exness-MT5Trial5": {"XAUUSD": true},
}

// swapFreeServer 判定 (server, broker_symbol) 是否属 swap-free 豁免（精确匹配）。
func swapFreeServer(server, symbol string) bool {
	set, ok := swapFreeWhitelist[server]
	return ok && (set["*"] || set[symbol])
}

// normalizeSwapBpsDaily 归一单方向 swap 原始值为日 bps（正=收，负=付）。
// ok=false 表示缺价格或未标定，调用方跳过该 Fact。
func normalizeSwapBpsDaily(mode SwapMode, s, price, point, contractSize float64) (float64, bool) {
	switch mode {
	case SwapModeNone:
		return 0, true
	case SwapModeInPoints:
		if price <= 0 {
			return 0, false
		}
		return s * point / price * 10000, true
	case SwapModePerLotCurrency:
		if price <= 0 || contractSize <= 0 {
			return 0, false
		}
		return s / (price * contractSize) * 10000, true
	case SwapModeAnnualPct:
		// S% 年化 / 365 天 → 日百分比 → bps
		return s / 100 / 365 * 10000, true
	default:
		return 0, false
	}
}

// swapNeedsPrice 该 mode 的归一化是否依赖报价（None/AnnualPct 与价格无关，冷启动仍可发 Fact）。
func swapNeedsPrice(mode SwapMode) bool {
	return mode == SwapModeInPoints || mode == SwapModePerLotCurrency
}

// mt5SwapMode 映射 MT5 ENUM_SYMBOL_SWAP_MODE（proto 透传原始值，D-058 实证数值对齐）。
// 完整映射表（proto 枚举 0~8 + FxPro 实证超界值 9）：
//
//	0 SwapNone         → SwapModeNone（真停用发 0 值 Fact；非零 rates=矛盾→Uncalibrated）
//	1 InPoints         → SwapModeInPoints（S × Points / price × 1e4）
//	2 SymInfo_s408     → SwapModePerLotCurrency（标的货币额，S / (price × CS) × 1e4；同 3/4 口径）
//	3 MarginCurrency   → SwapModePerLotCurrency（保证金货币额，同上）
//	4 Currency         → SwapModePerLotCurrency（入金/利润货币额，同上）
//	5 PercCurPrice     → SwapModeAnnualPct（S% / 365 × 1e2）
//	6 PercOpenPrice    → SwapModeAnnualPct（同 5，用开仓价≈现价）
//	7 PointClosePrice  → SwapModeUncalibrated（重开仓模式，点数±收盘价，非线性无法归一）
//	8 PointBidPrice    → SwapModeUncalibrated（重开仓模式，点数±买价，同上）
//	9 FxPro 扩展       → SwapModeInPoints（D-092 标定：同 broker 全部其他符号=InPoints 且
//	                    point/CS 同构；点数解读 ±$5.5~94/手/日 包住 Exness 同符号区间；
//	                    证据见 docs/油类SwapType9标定-施工提示词.md S1 表，2026-08-24 采集）
//	10+ 超界           → SwapModeUncalibrated（proto 未定义；计数日志禁静默）
func mt5SwapMode(t mt5pb.SwapType, long, short float64) SwapMode {
	switch t {
	case mt5pb.SwapType_SwapType_SwapNone:
		if long != 0 || short != 0 {
			return SwapModeUncalibrated // 矛盾数据
		}
		return SwapModeNone
	case mt5pb.SwapType_SwapType_InPoints, mt5pb.SwapType(9): // 9=FxPro 扩展 InPoints（D-092）
		return SwapModeInPoints
	case mt5pb.SwapType_SwapType_SymInfo_s408, mt5pb.SwapType_SwapType_MarginCurrency, mt5pb.SwapType_SwapType_Currency:
		return SwapModePerLotCurrency
	case mt5pb.SwapType_SwapType_PercCurPrice, mt5pb.SwapType_SwapType_PercOpenPrice:
		return SwapModeAnnualPct
	default: // PointClosePrice(7) / PointBidPrice(8) / 超界(10+)
		return SwapModeUncalibrated
	}
}

// swapTypeZeroAsInPoints D-104 P1 T1（W4a S2 施工）：PXBT(venue=1314615) SwapType=0 + 非零值 → InPoints 标定。
// 证据：44/45 品种 swapType=0 + 非零值，幅度交叉验证同量级同向（EURUSD long=-3.6961 点 vs Exness -0.50/IC -0.71/FxPro -0.76）。
// mode 标签错误（同 D-092 FxPro mode 9 类标定异常），真值是 InPoints 口径。
var swapTypeZeroAsInPoints = map[string]bool{
	"1314615": true, // PXBTTrading-1
}

// mt5SwapModeForVenue D-159 W4a S2：per-venue override + mt5SwapMode 纯函数。
// venue 在 swapTypeZeroAsInPoints + SwapType==SwapNone + 非零值 → InPoints（PXBT 标定）；
// 否则走 mt5SwapMode 原逻辑（不改 mt5SwapMode 签名，MT4 路径不受影响）。
func mt5SwapModeForVenue(venue string, t mt5pb.SwapType, long, short float64) SwapMode {
	if swapTypeZeroAsInPoints[venue] && t == mt5pb.SwapType_SwapType_SwapNone && (long != 0 || short != 0) {
		return SwapModeInPoints
	}
	return mt5SwapMode(t, long, short)
}
