// pairs_test.go —— D-472 S5：监控簿白名单派生矩阵（B-007 排除集/B-008 反向对账/
// B-009 覆盖≥现行 9 行防丢行 + paperCfdPairKey 口径镜像对拍）。
package main

import (
	"strings"
	"testing"
)

// d472Combos 生产 xasset_allow_combos enabled 行投影（2026-09-25 实拉，id 1-14 中 enabled=11 行）。
func d472Combos() []allowComboRow {
	return []allowComboRow{
		{"CLUSDT", "gate:perp", "WTI", "277842155:mt5"},         // id1：nonDerivable（原油期货×WTI 现货）
		{"XAUUSD", "277842155:mt5", "XAUUSDT", "gate:perp"},     // id2
		{"XAUUSD", "277842155:mt5", "XAUUSDT", "binance:perp"},  // id3
		{"XAUUSD", "277842155:mt5", "PAXGUSDT", "binance:perp"}, // id4
		{"XAUTUSDT", "binance:perp", "XAUUSDT", "binance:perp"}, // id5：同所（B-008 名单）
		{"PAXGUSDT", "binance:perp", "XAUUSDT", "binance:perp"}, // id6：同所（B-008 名单）
		{"BTCUSDT", "3001157:mt5", "BTCUSDT", "okx:perp"},       // id10（组合#10）
		{"XAUUSD", "3001157:mt5", "XAUUSDT", "okx:perp"},        // id11（pos502 同构）
		{"XAUUSD", "3001157:mt5", "XAUUSDT", "gate:perp"},       // id12（组合#12）
		{"XPTUSD", "3001157:mt5", "XPTUSDT", "gate:perp"},       // id13（组合#13）
		{"XPDUSD", "3001157:mt5", "XPDUSDT", "gate:perp"},       // id14（组合#14）
	}
}

// d472Universe 生产 fin_cex_universe TRADING 行投影（本票相关子集）。
func d472Universe() []universeRow {
	return []universeRow{
		{"gate", "XAU_USDT", "XAUUSD"},
		{"gate", "XPT_USDT", "XPTUSD"},
		{"gate", "XPD_USDT", "XPDUSD"},
		{"binance", "XAUUSDT", "XAUUSD"},
		{"binance", "PAXGUSDT", "XAUUSD"},
		{"okx", "XAU-USDT-SWAP", "XAUUSD"},
		{"okx", "BTC-USDT-SWAP", "BTCUSDT"},
	}
}

// currentNine 现行 monitorPairs() 硬编码 9 行三元组（B-009 防丢行对拍基线）。
// 口径统一={cexVenue, pair, exSym}（sym 由派生内定，非现行清单身份字段）。
func currentNine() [][3]string {
	return [][3]string{
		{"binance:perp", "PAXG-XAU", "XAUUSD"},
		{"binance:perp", "XAUT-XAU", "XAUUSD"},
		{"binance:perp", "XAUUSDT-XAU", "XAUUSD"},
		{"binance:perp", "BTCUSDT-BTC", "BTCUSDT"},
		{"okx:perp", "XAUUSDT-XAU", "XAUUSD"},
		{"okx:perp", "BTCUSDT-BTC", "BTCUSDT"},
		{"gate:perp", "XAUUSDT-XAU", "XAUUSD"},
		{"gate:perp", "XPTUSDT-XPT", "XPTUSD"},
		{"gate:perp", "XPDUSDT-XPD", "XPDUSD"},
	}
}

// TestD472_DeriveCoversCurrentNine B-009：派生∪legacy ⊇ 现行 9 行三元组（丢行=红）。
// 注：现行第 4 行（binance BTCUSDT-BTC）无 enabled 组合源——入 legacyPairs 第三条保守保留
// （B-009 对拍留痕，删行=决策席裁定）。
func TestD472_DeriveCoversCurrentNine(t *testing.T) {
	pairs, uncovered := deriveMonitorPairs(d472Combos(), d472Universe(), nil, nil, nil)
	have := map[string]bool{}
	for _, p := range pairs {
		have[p.cexVenue+"|"+p.sym+"|"+p.pair+"|"+p.exSym] = true
	}
	for _, want := range currentNine() {
		found := false
		for p := range have {
			parts := strings.SplitN(p, "|", 4) // cexVenue|sym|pair|exSym
			if parts[0] == want[0] && parts[2] == want[1] && parts[3] == want[2] {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("现行行丢失: %v（派生清单 %d 行 uncovered=%v）", want, len(pairs), uncovered)
		}
	}
}

// TestD472_DerivePairKeyPaperCfdMirror 派生键与 paperCfdPairKey 口径镜像（消费端同式）。
func TestD472_DerivePairKeyPaperCfdMirror(t *testing.T) {
	pairs, _ := deriveMonitorPairs(d472Combos(), d472Universe(), nil, nil, nil)
	want := map[string]bool{
		"XPTUSDT-XPT": false, "XPDUSDT-XPD": false, "BTCUSDT-BTC": false, "XAUUSDT-XAU": false,
	}
	for _, p := range pairs {
		if _, ok := want[p.pair]; ok {
			want[p.pair] = true
		}
	}
	for k, hit := range want {
		if !hit {
			t.Errorf("paperCfdPairKey 口径键缺失: %s", k)
		}
	}
}

// TestD472_UncoveredAndExclusions B-007/B-008：同所组合入名单；nonDerivable 静默排除；
// universe miss → fail-closed 暴露。
func TestD472_UncoveredAndExclusions(t *testing.T) {
	_, uncovered := deriveMonitorPairs(d472Combos(), d472Universe(), nil, nil, nil)
	joined := strings.Join(uncovered, "\n")
	if !strings.Contains(joined, "XAUTUSDT(binance:perp)×XAUUSDT(binance:perp)") {
		t.Errorf("同所组合 id5 应入反向对账名单: %v", uncovered)
	}
	if !strings.Contains(joined, "PAXGUSDT(binance:perp)×XAUUSDT(binance:perp)") {
		t.Errorf("同所组合 id6 应入反向对账名单: %v", uncovered)
	}
	if strings.Contains(joined, "CLUSDT") {
		t.Errorf("nonDerivable 行（CLUSDT×WTI）应静默排除不进名单: %v", uncovered)
	}
	// universe miss：撤 gate 行 → id12/13/14 fail-closed 暴露（不静默丢）
	uni := d472Universe()
	var noGate []universeRow
	for _, r := range uni {
		if r.venue != "gate" {
			noGate = append(noGate, r)
		}
	}
	_, unc2 := deriveMonitorPairs(d472Combos(), noGate, nil, nil, nil)
	if !strings.Contains(strings.Join(unc2, "\n"), "universe 无合约行") {
		t.Errorf("universe miss 应 fail-closed 暴露: %v", unc2)
	}
}

// TestD472_DeriveDeterminism 同输入两跑清单逐行一致（cron 轮稳定性）。
func TestD472_DeriveDeterminism(t *testing.T) {
	a, ua := deriveMonitorPairs(d472Combos(), d472Universe(), nil, nil, nil)
	b, ub := deriveMonitorPairs(d472Combos(), d472Universe(), nil, nil, nil)
	if len(a) != len(b) || len(ua) != len(ub) {
		t.Fatalf("派生不稳定: %d/%d vs %d/%d", len(a), len(ua), len(b), len(ub))
	}
	for i := range a {
		if a[i] != b[i] {
			t.Errorf("行 %d 漂移: %+v vs %+v", i, a[i], b[i])
		}
	}
}

// derivedPairs 集成测助手：生产形 fixture 派生清单（替代退役的 monitorPairs()）。
func derivedPairs() []monitorPair {
	pairs, _ := deriveMonitorPairs(d472Combos(), d472Universe(), nil, nil, nil)
	return pairs
}

// d144HasPair 派生清单按 (cex_venue,pair,exSym,mt_venue) 命中判定。
func d144HasPair(pairs []monitorPair, cexVenue, pair, exSym, mtKey string) bool {
	for _, p := range pairs {
		if p.cexVenue == cexVenue && p.pair == pair && p.exSym == exSym && p.mtVenueKey == mtKey {
			return true
		}
	}
	return false
}

// TestD144_UniverseDemandDerivesPair 锚①：universe 达标行无白名单匹配也派生簿对
// （旧口径=白名单独有→33 行纸面仓 basis 恒 NULL 根修）。
func TestD144_UniverseDemandDerivesPair(t *testing.T) {
	uni := append(d472Universe(), universeRow{"binance", "SOLUSDT", "SOL"})
	pairs, _ := deriveMonitorPairs(nil, uni,
		[]cfdDemandRow{{canonical: "SOL", mtVenue: "277842155", cexVenue: "binance"}},
		[]mtAcct{{venue: "277842155", vtype: "mt5"}}, nil)
	if !d144HasPair(pairs, "binance:perp", "SOLUSDT-SOL", "SOL", "277842155:mt5") {
		t.Fatalf("universe 达标行应派生簿对, pairs=%v", pairs)
	}
}

// TestD144_NewStreamAccountDerives 锚②：ticks 在流新账户→该账户键簿对（各账户独立采样）。
func TestD144_NewStreamAccountDerives(t *testing.T) {
	uni := append(d472Universe(), universeRow{"binance", "SOLUSDT", "SOL"})
	demand := []cfdDemandRow{{canonical: "SOL", mtVenue: "99999999", cexVenue: "binance"}}
	pairs, _ := deriveMonitorPairs(nil, uni, demand,
		[]mtAcct{{venue: "99999999", vtype: "mt5"}}, nil)
	if !d144HasPair(pairs, "binance:perp", "SOLUSDT-SOL", "SOL", "99999999:mt5") {
		t.Fatalf("新在流账户应派生其键簿对, pairs=%v", pairs)
	}
	// 禁借用：他账户键不得顶替本账户 demand（簿对 mt_venue 严格=行自账户）。
	if d144HasPair(pairs, "binance:perp", "SOLUSDT-SOL", "SOL", "3001157:mt5") {
		t.Fatal("demand 簿对不得借用他账户 venue 键")
	}
}

// TestD144_DisableAllowlistDemandRetains 锚④：白名单行禁用后上游 demand 仍保住簿对
// （10-02→10-04 白名单缩编致簿塌缩只剩 BTC legacy 的事故根修）。
func TestD144_DisableAllowlistDemandRetains(t *testing.T) {
	uni := d472Universe()
	accts := []mtAcct{{venue: "3001157", vtype: "mt5"}}
	demand := []cfdDemandRow{{canonical: "XAU", mtVenue: "3001157", cexVenue: "gate"}}
	withCombo, _ := deriveMonitorPairs(d472Combos(), uni, demand, accts, nil)
	withoutCombo, _ := deriveMonitorPairs(nil, uni, demand, accts, nil)
	for _, tag := range []struct {
		name  string
		pairs []monitorPair
	}{{"enabled", withCombo}, {"disabled", withoutCombo}} {
		found := false
		for _, p := range tag.pairs {
			if p.cexVenue == "gate:perp" && p.mtVenueKey == "3001157:mt5" && p.exSym == "XAU" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s 白名单时 gate XAU 簿对缺失, pairs=%v", tag.name, tag.pairs)
		}
	}
}
