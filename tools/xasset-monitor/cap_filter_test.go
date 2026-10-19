// cap_filter_test.go —— 组合×venue 展开能力滤锚（裁定注记53③/簿饥饿销账遗留②）：
// broker_symbol_map 无映射 canonical 不产对（TRXUSDT@110660304 类结构性 skip 噪音
// 根除）；nil caps=滤关（既有派生零行为变）；demand 绑定对不滤（仓驱动必须继续暴露
// ——死腿仓的覆盖告警面即保底）。
package main

import (
	"context"
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

// TestCapabilityFilter_ComboExpansion 主锚（夹具镜像 1055_s2canon_norm 后生产形态：
// 组合 MT 腿 canonical=裸形 TRX×map=全形 TRXUSDT——capable 键双侧过 bareOf 归一，
// 键域错位=在流真对假滤）。TRX 组合按 venue 集展开：110660304 无 TRX 映射→不产对；
// 15032313/3001157 有映射→照产且 pair 拼写不变。对抗锚：删 capable 过滤行→本测红
// （110660304 对复现）；裸形组合×尾缀 map 不产对=键域回归红。
func TestCapabilityFilter_ComboExpansion(t *testing.T) {
	// 生产实况镜像（SG 现役组合 #17-19 亲证）：MT 腿 canonical=裸形 TRX；
	// map.canonical=全形 TRXUSDT（broker_symbol_map 存储域）。
	combos := []allowComboRow{
		{"TRXUSDT", "binance:perp", "TRX", "15032313:mt5"},    // 1055 后裸形（现役）
		{"TRXUSDT", "binance:perp", "TRXUSDT", "3001157:mt5"}, // 1055 前全形（存量形态鲁棒）
	}
	uni := []universeRow{{"binance", "TRXUSDT", "TRXUSDT"}}
	accts := []mtAcct{
		{venue: "110660304", vtype: "mt5"},
		{venue: "15032313", vtype: "mt5"},
		{venue: "3001157", vtype: "mt5"},
	}
	// caps 域=loadSymbolCapabilities 产物（bareCanon 归一后裸形）——生产由 DB 读入
	// 时归一（全链锚见 TestCapabilityBuildNormalized），本夹具直接同域手建。
	caps := map[string]map[string]bool{
		"110660304": {"XAU": true, "WTI": true}, // 无 TRX=该户无此能力（生产实况）
		"15032313":  {"TRX": true, "XAU": true},
		"3001157":   {"TRX": true},
	}
	pairs, uncovered := deriveMonitorPairs(combos, uni, nil, accts, nil, caps)
	if !capVenueHas(pairs, "15032313:mt5", "TRXUSDT-TRX") || !capVenueHas(pairs, "3001157:mt5", "TRXUSDT-TRX") {
		t.Fatalf("裸形组合×尾缀 map 必须产对（键域归一锚）, pairs=%v", pairs)
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
	combos := []allowComboRow{{"TRXUSDT", "binance:perp", "TRX", "99999999:mt5"}} // 1055 后裸形
	uni := []universeRow{{"binance", "TRXUSDT", "TRXUSDT"}}
	accts := []mtAcct{{venue: "110660304", vtype: "mt5"}}
	caps := map[string]map[string]bool{"110660304": {"XAU": true}}
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
	caps := map[string]map[string]bool{"110660304": {"XAU": true}} // 无 SOL 映射
	pairs, _ := deriveMonitorPairs(nil, uni, demand, accts, nil, caps)
	if !d144HasPair(pairs, "binance:perp", "SOLUSDT-SOL", "SOL", "110660304:mt5") {
		t.Fatalf("demand 绑定对不得被能力滤剔除, pairs=%v", pairs)
	}
}

// TestCapabilityBuildNormalized 全链锚（返修对抗锚本体）：map 存储域全形
// （TRXUSD→TRXUSDT）→loadSymbolCapabilities 建键归一裸形→裸形组合（1055 后
// 现役形态）×该 caps 必产对——「裸形组合×尾缀 map 必须产对」真形态。
// 对抗锚：capable 查键去掉 bareOf 归一→本测红（在流真对假滤复现）。
func TestCapabilityBuildNormalized(t *testing.T) {
	s := d335r1Store(t)
	ctx := context.Background()
	t.Cleanup(func() {
		_, _ = s.Pool().Exec(ctx, `DELETE FROM broker_symbol_map WHERE source='captop-test'`)
		_, _ = s.Pool().Exec(ctx, `DELETE FROM broker_accounts WHERE venue='99999001'`)
	})
	if _, err := s.Pool().Exec(ctx, `DELETE FROM broker_symbol_map WHERE source='captop-test'`); err != nil {
		t.Fatal(err)
	}
	// 测试库 broker_accounts 空（FK 守门）——插最小编户行挂专属假户。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_accounts (venue, host, port, password, venue_type)
		 VALUES ('99999001','captop.invalid',443,'x','mt5')`); err != nil {
		t.Fatal(err)
	}
	// venue 走真户（FK→broker_accounts）；broker_symbol 用专属假名防撞现役行。
	if _, err := s.Pool().Exec(ctx,
		`INSERT INTO broker_symbol_map (venue, broker_symbol, canonical, product_group, is_primary, source)
		 VALUES ('99999001','CAPTOPTEST','CAPTOPTESTUSDT','cfd_spot',true,'captop-test')`); err != nil {
		t.Fatal(err)
	}
	caps, err := loadSymbolCapabilities(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if !caps["99999001"]["CAPTOPTEST"] {
		t.Fatalf("建键须归一裸形（CAPTOPTESTUSDT→CAPTOPTEST）, got=%v", caps["99999001"])
	}
	if caps["99999001"]["CAPTOPTESTUSDT"] {
		t.Fatal("caps 建键禁留全形（查侧裸形必 miss=错域假滤）")
	}
	// 全链双形态：1055 后裸形组合（现役）+1055 前全形组合（存量）×该 caps 都必须
	// 产对——两腿判据异键：裸形腿 exSym=CAPTOPTEST 咬「建侧归一」（caps 键须裸形）；
	// 全形腿 exSym=CAPTOPTESTUSDT 咬「查侧归一」（lookup 须 bareOf）。缺任一侧
	// 归一，对应腿消失=本测红（对抗消解：查侧去 bareOf→全形腿红；建侧去
	// bareCanon→裸形腿红——双侧均已实证）。
	combos := []allowComboRow{
		{"CAPTOPTESTUSDT", "binance:perp", "CAPTOPTEST", "99999001:mt5"},
		{"CAPTOPTESTUSDT", "binance:perp", "CAPTOPTESTUSDT", "99999001:mt5"},
	}
	uni := []universeRow{{"binance", "CAPTOPTESTUSDT", "CAPTOPTESTUSDT"}}
	accts := []mtAcct{{venue: "99999001", vtype: "mt5"}}
	pairs, _ := deriveMonitorPairs(combos, uni, nil, accts, nil, caps)
	hasTriple := func(key, pair, exSym string) bool {
		for _, p := range pairs {
			if p.mtVenueKey == key && p.pair == pair && p.exSym == exSym {
				return true
			}
		}
		return false
	}
	if !hasTriple("99999001:mt5", "CAPTOPTESTUSDT-CAPTOPTEST", "CAPTOPTEST") {
		t.Fatalf("裸形组合×尾缀 map 必须产对（建侧归一锚）, pairs=%v", pairs)
	}
	if !hasTriple("99999001:mt5", "CAPTOPTESTUSDT-CAPTOPTEST", "CAPTOPTESTUSDT") {
		t.Fatalf("全形组合×归一 caps 必须产对（查侧归一锚）, pairs=%v", pairs)
	}
}
