// okx_ws_delivery.go —— D-649续④ okx USD_UM 交割订阅面（自 okx_ws.go 折分，
// 行数门禁）：armed 组合 futures 腿 instId 目录裁决订阅+tickers 帧分流键。
// watchlist 方言件只产 spot/perp 候选——交割 instId 由毕业档 provenance 注入。
package exchange

import (
	"context"
	"log"
	"strings"
)

// DeliverySub armed 交割订阅行：InstID=okx FUTURES instId（'ETH-USD-261225'
// 形），Canonical=bus canonical（detector hint 同键——armed 组合 canonical_sell）。
type DeliverySub struct {
	InstID    string
	Canonical string
}

// DelivInstID 毕业档合约名→okx FUTURES instId（'ETH-USD_UM-261225'→
// 'ETH-USD-261225'；_UM=USD 本位标记剥离，非 _UM 形原样透传）。装配层单源。
func DelivInstID(sym string) string {
	return strings.ReplaceAll(sym, "_UM-", "-")
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
