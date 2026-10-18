// live_equity.go —— 浓度透视分母实拉注件（判定径快照读清账）：装配=cmd/core
// acctMon.Refresh 扇出 venue:type:ccy→equity 单次实拉；拍首单次扇出禁 TTL 变相快照。
package dciexec

import (
	"context"
	"errors"
)

var liveEquityMapFn func(context.Context) (map[string]float64, error)

// SetLiveEquityMapFn cmd/core 装配注入（oppmatch/budget 同径）。
func SetLiveEquityMapFn(fn func(context.Context) (map[string]float64, error)) { liveEquityMapFn = fn }

// LiveEquityMap 装配期持有件——调用拍内解包级注件（Executor 早构注件晚注册零竞）。
func LiveEquityMap(ctx context.Context) (map[string]float64, error) {
	if liveEquityMapFn == nil {
		return nil, errors.New("live equity map 未接线")
	}
	return liveEquityMapFn(ctx)
}
