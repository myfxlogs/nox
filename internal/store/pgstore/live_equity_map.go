// live_equity_map.go —— 「此刻权益」实拉注件（判定径快照读清账）：
// 装配=cmd/core acctMon.Refresh 扇出 venue:type:ccy→equity；nil=快照行值保留
// （装配缺席非病灶——消费面如实缺语义）。拍首单次扇出沿用禁逐池 Refresh。
package pgstore

import "context"

var liveEquityMapFn func(context.Context) (map[string]float64, error)

// SetLiveEquityMapFn cmd/core 装配注入（oppmatch/budget/dciexec 同径）。
func SetLiveEquityMapFn(fn func(context.Context) (map[string]float64, error)) { liveEquityMapFn = fn }

// liveEquity 单键实拉取值：装配+map 含键→live 值；缺席→快照原值如实保。
func liveEquity(ctx context.Context, venue, venueType, ccy string, fallback float64) float64 {
	if liveEquityMapFn == nil {
		return fallback
	}
	m, err := liveEquityMapFn(ctx)
	if err != nil || m == nil {
		return fallback // 实拉败=快照行值如实保（非清零——判径零虚构）
	}
	if v, ok := m[venue+":"+venueType+":"+ccy]; ok {
		return v
	}
	return fallback
}
