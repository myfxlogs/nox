// live_equity_map.go —— 「此刻权益」实拉注件（判定径快照读清账）：
// 装配=cmd/core acctMon.Refresh 扇出 venue:type:ccy→equity；nil=快照行值保留
// （装配缺席非病灶——消费面如实缺语义）。拍首单次扇出沿用禁逐池 Refresh。
package pgstore

import "context"

var liveEquityMapFn func(context.Context) (map[string]float64, error)

// SetLiveEquityMapFn cmd/core 装配注入（oppmatch/budget/dciexec 同径）。
func SetLiveEquityMapFn(fn func(context.Context) (map[string]float64, error)) { liveEquityMapFn = fn }
