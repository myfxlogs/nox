package exec

import (
	"context"
	"fmt"
	"log"

	"arb/internal/fact"
)

// checkTradeMode MT 腿 trade_mode 判定（D-004 P1#2）：判定时刻即时探针取值，
// 不读 1h 采集 facts——venue 服务端返回值为唯一权威。
// 开仓 fail-closed：prober 未接线/取值失败/不可交易模式一律拒开；
// 平仓减风险：取值失败放行+告警日志（venue 拒单为最终权威），仅 DISABLED 否决。
func checkTradeMode(ctx context.Context, prober TradeModeProber, tickVenue, tickSymbol, brokerSymbol, side string, closing bool) (bool, string) {
	if prober == nil {
		if closing {
			log.Printf("[exec] %s@%s trade_mode prober not wired, closing allowed (venue authoritative)", tickSymbol, tickVenue)
			return true, ""
		}
		return false, fmt.Sprintf("%s@%s trade_mode prober not wired", tickSymbol, tickVenue)
	}
	mode, err := prober.TradeModeNow(ctx, brokerSymbol)
	if err != nil {
		if closing {
			log.Printf("[exec] %s@%s trade_mode unknown: %v, closing allowed (venue authoritative)", tickSymbol, tickVenue, err)
			return true, ""
		}
		return false, fmt.Sprintf("%s@%s trade_mode unknown: %v", tickSymbol, tickVenue, err)
	}
	switch {
	case int64(mode) == fact.TradeModeDisabled:
		return false, fmt.Sprintf("%s@%s trade_mode=disabled", tickSymbol, tickVenue)
	case int64(mode) == fact.TradeModeCloseOnly && !closing:
		return false, fmt.Sprintf("%s@%s trade_mode=close_only", tickSymbol, tickVenue)
	case int64(mode) == fact.TradeModeLongOnly && !closing && side == "SELL":
		return false, fmt.Sprintf("%s@%s trade_mode=long_only side=%s", tickSymbol, tickVenue, side)
	case int64(mode) == fact.TradeModeShortOnly && !closing && side == "BUY":
		return false, fmt.Sprintf("%s@%s trade_mode=short_only side=%s", tickSymbol, tickVenue, side)
	}
	return true, ""
}
