package exec

import (
	"context"
	"log"
	"net/url"
	"strings"
)

// binanceDualSide 探测 Binance perp 账户双仓模式（GET /fapi/v1/positionSide/dual）。
// 结果缓存于 executor 实例（dualSide/dualSideProbed），避免每单探测。
// 探测失败 → 返回 (false, false)，调用方保守按 hedge 处理（D-108 P2 T1）。
// dualSide=true=hedge（双仓），dualSide=false=one-way（单仓）。
func (e *CEXExecutor) binanceDualSide(ctx context.Context) (dual, probed bool) {
	e.dualSideMu.Lock()
	defer e.dualSideMu.Unlock()
	if e.dualSideProbed {
		return e.dualSide, true
	}
	params := url.Values{}
	body, err := e.signedBinanceRequest(ctx, "GET", "/v1/positionSide/dual", params)
	if err != nil {
		log.Printf("[cex] binance dualSide probe failed venue=%s: %v（保守按 hedge 处理）", e.venue, err)
		return false, false // 未探测成功，保守 hedge
	}
	dual, _ = body["dualSidePosition"].(bool)
	e.dualSide = dual
	e.dualSideProbed = true
	log.Printf("[cex] binance dualSide probe venue=%s dual=%v", e.venue, dual)
	return dual, true
}

// binancePosSide 将内部 PositionSide（long/short 或 BUY/SELL）映射为 Binance positionSide 参数值。
// Binance hedge 模式 positionSide 仅接受 "LONG"/"SHORT"（大写）。
func binancePosSide(ps string) string {
	switch strings.ToLower(ps) {
	case "long", "buy":
		return "LONG"
	case "short", "sell":
		return "SHORT"
	default:
		return strings.ToUpper(ps)
	}
}
