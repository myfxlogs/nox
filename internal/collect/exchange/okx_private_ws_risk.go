// okx_private_ws_risk.go —— D-566 包B S7：OKX 私有流风险频道（liquidation-warning）。
// 帧到达 → OnLiqWarning(instID,posSide,mgnRatio) 三键透传（缺席空串不猜）→
// 装配层 cexAlertFn critical（binance MARGIN_CALL 同通道）。
package exchange

import (
	"encoding/json"
	"log"
)

// okxLiqWarnConsumed liquidation-warning data[] 已核对字段白名单（设计 §2.7 官方
// 全字段集——D-475b 契约：白名单=已核对清单非仅消费清单，欠列=核对未做）。
var okxLiqWarnConsumed = []string{
	"instId", "instType", "instFamily", "posId", "posSide", "pos", "posCcy",
	"lever", "markPx", "mgnRatio", "ccy", "cTime", "uTime", "pTime",
}

// onLiqWarning liquidation-warning 帧 → 逐条三键回调（nil hook 安全跳过）。
func (o *OKXPrivateWS) onLiqWarning(data []map[string]any) {
	for _, it := range data {
		if o.hooks.OnLiqWarning != nil {
			o.hooks.OnLiqWarning(str(it["instId"]), str(it["posSide"]), str(it["mgnRatio"]))
		}
	}
	if raw, err := json.Marshal(data); err == nil {
		WarnUnconsumed("okx", "liquidation-warning", raw, okxLiqWarnConsumed)
	} else {
		log.Printf("[okx-ws] liq-warning marshal: %v", err)
	}
}

// okxPrivateSubscribeArgs D-566 包B：私有订阅 args 单帧形制（S0 真户实证，各 ack 独立
// event=subscribe）——提取为纯函数供锚测试钉死清单。
func okxPrivateSubscribeArgs() []map[string]string {
	return []map[string]string{
		{"channel": "account", "ccy": "USDT"},
		{"channel": "balance_and_position"},
		{"channel": "positions", "instType": "SWAP"},
		{"channel": "orders", "instType": "SWAP"},
		// D-566 包B S4-a：SPOT orders 同频道异 instType 各一 arg。
		{"channel": "orders", "instType": "SPOT"},
		// D-566 包B S7：爆仓预警（instType=ANY 全域）。
		{"channel": "liquidation-warning", "instType": "ANY"},
	}
}
