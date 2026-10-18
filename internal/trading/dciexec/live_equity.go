// live_equity.go —— 浓度透视分母实拉注件（D-604 判定点读径——快照池节拍×
// 鲜度阈错位的伪「分母池鲜度失」族根治）：装配=cmd/core acctMon.Refresh 扇出
// venue:type:ccy→equity 单次实拉（拉败=池跳过+warn 留痕；禁 TTL 变相快照）。
package dciexec

import (
	"context"
	"errors"
)

var liveEquityMapFn func(context.Context) (map[string]float64, error)

// SetLiveEquityMapFn cmd/core 装配注入（acctMon.Refresh 扇出同款——oppmatch/budget 同径）。
func SetLiveEquityMapFn(fn func(context.Context) (map[string]float64, error)) { liveEquityMapFn = fn }

// LiveEquityMap 装配期持有件——调用拍内解包级注件（Executor 早期构、注件晚注册的
// 装配时序零竞）。未接线=err（消费面 warn 剔分母非静默）。
func LiveEquityMap(ctx context.Context) (map[string]float64, error) {
	if liveEquityMapFn == nil {
		return nil, errors.New("live equity map 未接线")
	}
	return liveEquityMapFn(ctx)
}
