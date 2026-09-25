// field_diff.go D-475b S2：WS 帧未消费关键字段 diff 告警（在线观测面）。
// 纯函数零状态写面：帧里存在 critical 键但不在处理器 consumed 列表→warn log。
// 只观测不拦——一期后视噪声水位决定升降级（>10条/天回评审节流/清单裁剪）。
// 节流=同 venue.channel.key 1h 去重（map+mutex；去重时间源可注入供测试）。
package exchange

import (
	"encoding/json"
	"log"
	"sync"
	"time"
)

// criticalFieldKeys 帧级关键字段白名单（venue 各通道命名合集）。
// 帧含这些键但处理器没消费=潜在静默降解（OKX upl 失明实证起源）。
var criticalFieldKeys = map[string]bool{
	"upl": true, "unrealised_pnl": true, "unRealizedProfit": true, "up": true,
	"pnl": true, "profit": true, "fee": true, "commission": true,
	"lever": true, "liqPx": true, "margin": true, "mmr": true, "imr": true,
	"availEq": true, "isolatedWallet": true, "notional": true,
}

// fieldDiffSeen 1h 去重表（venue.channel.key→上次告警时刻）。
var (
	fieldDiffSeen = map[string]time.Time{}
	fieldDiffMu   sync.Mutex
	// fieldDiffNow 时间源注入 seam（测试压缩 1h 窗用）。
	fieldDiffNow = time.Now
)

// WarnUnconsumed 帧 raw JSON 递归扫 critical 键：存在且不在 consumed → warn。
// consumed=处理器实际读取的顶层/嵌套键名列表（函数级 const 声明在调用点）。
// raw 非合法 JSON→静默跳过（解析器已在其职责面报错，此处不重复噪音）。
func WarnUnconsumed(venue, channel string, raw json.RawMessage, consumed []string) {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return
	}
	consumedSet := make(map[string]bool, len(consumed))
	for _, k := range consumed {
		consumedSet[k] = true
	}
	var hits []string
	seen := map[string]bool{}
	walkJSON(v, func(k string) {
		if criticalFieldKeys[k] && !consumedSet[k] && !seen[k] {
			seen[k] = true
			hits = append(hits, k)
		}
	})
	for _, k := range hits {
		dedup := venue + "." + channel + "." + k
		fieldDiffMu.Lock()
		if at, ok := fieldDiffSeen[dedup]; ok && fieldDiffNow().Sub(at) < time.Hour {
			fieldDiffMu.Unlock()
			continue
		}
		fieldDiffSeen[dedup] = fieldDiffNow()
		fieldDiffMu.Unlock()
		log.Printf("[field-diff] %s.%s 帧含未消费关键字段: %s", venue, channel, k)
	}
}

// walkJSON 递归遍历 map/array 收集所有对象键名。
func walkJSON(v any, fn func(k string)) {
	switch t := v.(type) {
	case map[string]any:
		for k, vv := range t {
			fn(k)
			walkJSON(vv, fn)
		}
	case []any:
		for _, vv := range t {
			walkJSON(vv, fn)
		}
	}
}
