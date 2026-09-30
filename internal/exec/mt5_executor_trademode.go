package exec

import (
	"context"
	"errors"
	"fmt"
	"time"

	mt5pb "arb/internal/adapter/gen/mtapi/mt5"
)

// tradeModeProbeTimeout 执行前 trade_mode 即时探针超时（D-004 P1#2）：
// 判定时刻直查 venue，超时=取值失败 → 开仓 fail-closed。
const tradeModeProbeTimeout = 3 * time.Second

// TradeModeNow 判定时刻即时取该符号可交易模式（fact.TradeMode* 口径 0-4）。
// 单符号 SymbolParams 直查 venue，不消费采集缓存（D-004）；RPC 错/空返回/
// venue 错误/缺符号数据 → 返 error，由调用方按开/平仓分支决定去留。
func (e *MT5Executor) TradeModeNow(ctx context.Context, brokerSymbol string) (int64, error) {
	pctx, cancel := context.WithTimeout(ctx, tradeModeProbeTimeout)
	defer cancel()
	resp, err := e.mt5.SymbolParams(pctx, &mt5pb.SymbolParamsRequest{Id: e.token, Symbol: brokerSymbol})
	if err != nil {
		return 0, fmt.Errorf("mt5 symbol params: %w", err)
	}
	if resp == nil || resp.GetResult() == nil {
		return 0, errors.New("mt5 symbol params: empty result")
	}
	if e := resp.GetError(); e != nil {
		return 0, fmt.Errorf("mt5 symbol params: [code=%s] %s", e.GetCode().String(), e.GetMessage())
	}
	sg := resp.GetResult().GetSymbolGroup()
	if sg == nil {
		return 0, fmt.Errorf("mt5 symbol params: no symbol group for %s", brokerSymbol)
	}
	return int64(sg.GetTradeMode()), nil
}
