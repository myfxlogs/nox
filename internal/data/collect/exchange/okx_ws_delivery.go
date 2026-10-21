// okx_ws_delivery.go —— D-649续④ okx USD_UM 交割订阅面（自 okx_ws.go 折分，
// 行数门禁）：armed 组合 futures 腿 instId 目录裁决订阅+tickers 帧分流键。
// watchlist 方言件只产 spot/perp 候选——交割 instId 由毕业档 provenance 注入。
package exchange

import (
	"context"
	"log"
)

// DeliverySub armed 交割订阅行：InstID=okx FUTURES instId verbatim（毕业档
// 合约名本体——'ETH-USD_UM-261225'），Canonical=行情 canonical 发布键
// （'ETHUSDT' 形——与 spot 帧同键；D-649续④返修③：快照域=q.Symbol，双腿
// 异键永不同帧，发布键必须归行情键）。
type DeliverySub struct {
	InstID    string
	Canonical string
}

// DelivInstID 毕业档合约名→okx FUTURES instId（恒等透传——'_UM' 是 instId
// 本体构件：ETH-USD-261225（币本位）与 ETH-USD_UM-261225（USD 本位）目录并存
// 两张合约，剥尾=静默订错腿。退回②伤①锚）。
func DelivInstID(sym string) string {
	return sym
}

// deliverySubArgs armed 交割腿订阅 args：instId 目录裁决（FUTURES 类并入目录
// 集——catalog.fetch 已扩型）；delivCanon 映射每次重建（订阅面变动即刷）。
func (o *OKXWebSocket) deliverySubArgs(ctx context.Context, subscribed, quarantined map[string]bool) []map[string]string {
	o.delivCanon = map[string]string{}
	if o.DeliverySymbolsFn == nil {
		return nil
	}
	var args []map[string]string
	for _, ds := range o.DeliverySymbolsFn(ctx) {
		if ds.InstID == "" || ds.Canonical == "" {
			continue
		}
		if o.catalog.loaded() && !o.catalog.has(ds.InstID) {
			if !quarantined["fut:"+ds.InstID] {
				quarantined["fut:"+ds.InstID] = true
				log.Printf("[okx-ws] futures instId %s 不在 okx instruments 目录，跳过订阅", ds.InstID)
			}
			continue
		}
		key := ds.InstID + "|fut"
		if subscribed[key] {
			continue
		}
		subscribed[key] = true
		o.delivCanon[ds.InstID] = ds.Canonical
		args = append(args, map[string]string{"channel": "tickers", "instId": ds.InstID})
	}
	return args
}
