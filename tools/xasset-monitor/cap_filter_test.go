// cap_filter_test.go —— 组合×venue 展开能力滤锚（裁定注记53③/簿饥饿销账遗留②）：
// broker_symbol_map 无映射 canonical 不产对（TRXUSDT@110660304 类结构性 skip 噪音
// 根除）；nil caps=滤关（既有派生零行为变）；demand 绑定对不滤（仓驱动必须继续暴露
// ——死腿仓的覆盖告警面即保底）。
package main

import (
	"strings"
	"testing"
)

func capVenueHas(pairs []monitorPair, key, pair string) bool {
	for _, p := range pairs {
		if p.mtVenueKey == key && p.pair == pair {
			return true
		}
	}
	return false
}

// TestCapabilityFilter_ComboExpansion 主锚：TRX 组合（binance×15032313）按 venue 集
// 展开——110660304 无 TRXUSDT 映射→不产对；15032313/3001157 有映射→照产
// （生产实况镜像：110660304 map 无 TRXUSD 行、ticks 零行=每拍 skip 噪音源）。
// 对抗锚：删 capable 过滤行→本测红（110660304 对复现）。
func TestCapabilityFilter_ComboExpansion(t *testing.T) {
	combos := []allowComboRow{{"TRXUSDT", "binance:perp", "TRXUSDT", "15032313:mt5"}}
	uni := []universeRow{{"binance", "TRXUSDT", "TRXUSDT"}}
	accts := []mtAcct{
		{venue: "110660304", vtype: "mt5"},
		{venue: "15032313", vtype: "mt5"},
		{venue: "3001157", vtype: "mt5"},
	}
	caps := map[string]map[string]bool{
		"110660304": {"XAUUSD": true, "WTI": true}, // 无 TRXUSDT=该户无此能力（生产实况）
		"15032313":  {"TRXUSDT": true, "XAUUSD": true},
		"3001157":   {"TRXUSDT": true},
	}
	pairs, uncovered := deriveMonitorPairs(combos, uni, nil, accts, nil, caps)
	if !capVenueHas(pairs, "15032313:mt5", "TRXUSDT-TRX") || !capVenueHas(pairs, "3001157:mt5", "TRXUSDT-TRX") {
		t.Fatalf("有映射账户应照产 TRX 簿对, pairs=%v", pairs)
	}
	if capVenueHas(pairs, "110660304:mt5", "TRXUSDT-TRX") {
		t.Fatalf("无映射账户不得产对（结构性 skip 噪音源）, pairs=%v", pairs)
	}
	if len(uncovered) != 0 {
		t.Fatalf("组合未被全剔不应入反向对账, uncovered=%v", uncovered)
	}
	// 滤关锚：nil caps=过滤关——同输入三账户全产（既有行为零变）。
	all, _ := deriveMonitorPairs(combos, uni, nil, accts, nil, nil)
	if !capVenueHas(all, "110660304:mt5", "TRXUSDT-TRX") {
		t.Fatalf("nil caps 滤关应三账户全产, pairs=%v", all)
	}
}

// TestCapabilityFilter_AllFilteredUncovered 组合展开全被能力滤剔→反向对账名单暴露
// 不静默（派生面自身的 accounting 纪律：静默丢对=下一个「簿塌缩无人知」）。
func TestCapabilityFilter_AllFilteredUncovered(t *testing.T) {
	combos := []allowComboRow{{"TRXUSDT", "binance:perp", "TRXUSDT", "99999999:mt5"}}
	uni := []universeRow{{"binance", "TRXUSDT", "TRXUSDT"}}
	accts := []mtAcct{{venue: "110660304", vtype: "mt5"}}
	caps := map[string]map[string]bool{"110660304": {"XAUUSD": true}}
	pairs, uncovered := deriveMonitorPairs(combos, uni, nil, accts, nil, caps)
	if capVenueHas(pairs, "110660304:mt5", "TRXUSDT-TRX") || capVenueHas(pairs, "99999999:mt5", "TRXUSDT-TRX") {
		t.Fatalf("全无能力不得产对, pairs=%v", pairs)
	}
	flagged := false
	for _, u := range uncovered {
		if strings.Contains(u, "无能力 venue") && strings.Contains(u, "TRXUSDT") {
			flagged = true
		}
	}
	if !flagged {
		t.Fatalf("全剔组合应入反向对账名单, uncovered=%v", uncovered)
	}
}

// TestCapabilityFilter_DemandUnfiltered demand 绑定对不受能力滤（仓驱动必须继续
// 暴露——死腿仓退出判定/覆盖告警面在簿对在册的前提上，能力滤只治「无仓空展开」）。
func TestCapabilityFilter_DemandUnfiltered(t *testing.T) {
	uni := append(d472Universe(), universeRow{"binance", "SOLUSDT", "SOL"})
	demand := []cfdDemandRow{{canonical: "SOL", mtVenue: "110660304", cexVenue: "binance"}}
	accts := []mtAcct{{venue: "110660304", vtype: "mt5"}}
	caps := map[string]map[string]bool{"110660304": {"XAUUSD": true}} // 无 SOL 映射
	pairs, _ := deriveMonitorPairs(nil, uni, demand, accts, nil, caps)
	if !d144HasPair(pairs, "binance:perp", "SOLUSDT-SOL", "SOL", "110660304:mt5") {
		t.Fatalf("demand 绑定对不得被能力滤剔除, pairs=%v", pairs)
	}
}
