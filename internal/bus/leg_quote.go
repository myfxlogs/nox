package bus

import (
	"arb/internal/keydomain"
	"strings"
)

// CexSymbolKey venue 原生 CEX symbol → qbus 采集键归一（与采集器产出同构=权威键，D-481）：
// binance TRXUSDT → TRXUSDT；okx TRX-USDT-SWAP → TRXUSDT（去 -SWAP + 替换 -）；
// gate CL_USDT → CLUSDT（替换 _，镜像 canonicalFromGate=ReplaceAll("_","")，D-311 R6④）。
// D-481：自 dashboard cexCanonical 上移为单源——qbus 键=采集器产出形，消费侧禁第二份拷贝。
func CexSymbolKey(symbol string) string {
	s := strings.TrimSuffix(symbol, "-SWAP")
	s = strings.ReplaceAll(s, "-", "")
	return strings.ReplaceAll(s, "_", "")
}

// IsCexVenueKey venueKey 后缀判定 CEX 通道（:perp/:spot）。
// MT 键（:mt4/:mt5）的 qbus 键=canonical 本体（symbol map 发射），不适用原生键优先。
func IsCexVenueKey(venueKey string) bool {
	return keydomain.IsType(venueKey, "perp") || keydomain.IsType(venueKey, "spot")
}

// LatestLeg 腿级报价查找（D-481）：CEX 腿先查原生 symbol 归一键（采集器权威产出，
// 如 okx XAU-USDT-SWAP→XAUUSDT），miss 回退 canonical；MT 腿 D-571C2 S3 原生键优先——
// emitBlocked 变体（XAUUSD247m）按原生 brokerSym 发布，canonical 查永 miss=现价冻结
// （2555274799 形态）；非 blocked 按 canonical 发 → 原生 miss → canonical 兜底，双向覆盖。
// canonical/native 同形去重；任一键为空串走 Latest 正常 miss。
func (b *QuoteBus) LatestLeg(canonical, nativeSymbol, venueKey string) (Quote, bool) {
	if IsCexVenueKey(venueKey) {
		if nk := CexSymbolKey(nativeSymbol); nk != "" && nk != canonical {
			if q, ok := b.Latest(nk, venueKey); ok {
				return q, true
			}
		}
	} else if nativeSymbol != canonical {
		if q, ok := b.Latest(nativeSymbol, venueKey); ok {
			return q, true
		}
	}
	return b.Latest(canonical, venueKey)
}
