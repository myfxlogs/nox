// pairs.go D-423→D-472 S5：监控对清单派生（硬编码 9 行 vs xasset_allow_combos 14 行
// 双真相源漂移实证——okx XPT/XPD 簿缺席告警 9-25 实发——重写为白名单表派生）。
// 派生键=消费端权威口径 paperCfdPairKey（internal/scan/paper_all_in.go）同公式镜像：
// pair = cexLeg.canonical + "-" + bare(mtLeg.canonical)，bare=剥 "USDT" 优先 else "USD"
// （"XPT"→"XPTUSDT-XPT"、"BTC"→"BTCUSDT-BTC"、"XAUUSD"→"XAUUSDT-XAU" 与簿行实值一致）。
// 白名单加行≤5min（cron 轮重查表）簿覆盖自动跟上，零代码触碰。
package main

import "strings"

// monitorPair D-335R1 S1 + D-423 venue 化：{cexVenue, sym, pair, exSym}——
// cexVenue 路由 CEX 报价源（binance:perp=经 cex-proxy fapi；okx:perp=OKX 公开行情），
// 行级 venue 三元组落库（pair,cex_venue,mt_venue），消费端按仓 venue 直查禁借用。
type monitorPair struct{ cexVenue, sym, pair, exSym string }

// allowComboRow 派生入参：xasset_allow_combos enabled 行投影。
type allowComboRow struct{ canonicalBuy, venueBuy, canonicalSell, venueSell string }

// universeRow 派生入参：fin_cex_universe（status='TRADING'）行投影。
type universeRow struct{ venue, symbol, hedgeCanonical string }

// legacyPairs D-472 S5：跨资产簿常量保留（PAXG-XAU/XAUT-XAU=金属代币对 XAU 现货价，
// 非消费端 paperCfdPairKey 口径=不可派生行，注释标 legacy；消费方未全审=保守保留）。
var legacyPairs = []monitorPair{
	{"binance:perp", "PAXGUSDT", "PAXG-XAU", "XAUUSD"},
	{"binance:perp", "XAUTUSDT", "XAUT-XAU", "XAUUSD"},
	// D-335/D-419 历史观察行（现行 9 行对拍中唯一无 enabled 组合源者；B-009 保守保留，
	// 删行=决策席裁定）。
	{"binance:perp", "BTCUSDT", "BTCUSDT-BTC", "BTCUSDT"},
}

// nonDerivablePairs D-472 B-007：跨资产/非加密对显式排除集（fin_cex_universe 无合约行
// =结构性不可派生；列名跳过、不进 miss warn 与反向对账名单——防每轮刷屏）。
// 键=canonicalBuy+"|"+canonicalSell（双向命中）。
var nonDerivablePairs = map[string]bool{
	"CLUSDT|WTI": true, // D-330 demo 组合：原油期货 perp×WTI 现货
}

// deriveMonitorPairs D-472 S5：白名单派生（纯函数，可测）。
// combos=enabled 行；uni=TRADING 行。返回 派生清单（∪legacy，三元组去重）+
// 反向对账 uncovered 组合名（B-008：enabled 行未被 derived∪legacy 覆盖者——含双腿皆
// CEX 的同所组合；nonDerivable 命名行除外——结构性已知不刷屏）。
func deriveMonitorPairs(combos []allowComboRow, uni []universeRow) (pairs []monitorPair, uncovered []string) {
	byKey := map[string]string{} // venue|hc → symbol
	symsByVenue := map[string]map[string]bool{}
	for _, r := range uni {
		byKey[r.venue+"|"+r.hedgeCanonical] = r.symbol
		if symsByVenue[r.venue] == nil {
			symsByVenue[r.venue] = map[string]bool{}
		}
		symsByVenue[r.venue][r.symbol] = true
	}
	isMT := func(v string) bool { return strings.HasSuffix(v, ":mt4") || strings.HasSuffix(v, ":mt5") }
	bareOf := func(c string) string {
		if b := strings.TrimSuffix(c, "USDT"); b != c {
			return b
		}
		return strings.TrimSuffix(c, "USD")
	}
	// cexSymFor：universe 命中（venue+hedge_canonical）→ miss 按 venue 约定构造+回查
	// 存在性 → 仍 miss=false（fail-closed 不静默丢，调用方 skip+warn）。
	cexSymFor := func(cexVenueKey, mtCanonical, bare string) (string, bool) {
		v := cexVenueKey
		if i := strings.Index(v, ":"); i >= 0 {
			v = v[:i]
		}
		if s, ok := byKey[v+"|"+mtCanonical]; ok {
			return s, true
		}
		var guess string
		switch v {
		case "okx":
			guess = bare + "-USDT-SWAP"
		case "gate":
			guess = bare + "_USDT"
		case "binance":
			guess = bare + "USDT"
		default:
			return "", false
		}
		if symsByVenue[v][guess] {
			return guess, true
		}
		return "", false
	}

	pairs = make([]monitorPair, 0, len(legacyPairs)+len(combos))
	seen := map[string]bool{}
	for _, p := range legacyPairs {
		k := p.cexVenue + "|" + p.pair + "|" + p.exSym
		if !seen[k] {
			seen[k] = true
			pairs = append(pairs, p)
		}
	}
	for _, c := range combos {
		name := c.canonicalBuy + "(" + c.venueBuy + ")×" + c.canonicalSell + "(" + c.venueSell + ")"
		if nonDerivablePairs[c.canonicalBuy+"|"+c.canonicalSell] || nonDerivablePairs[c.canonicalSell+"|"+c.canonicalBuy] {
			continue // B-007：结构性不可派生，显式排除（不派生不警告）
		}
		buyMT, sellMT := isMT(c.venueBuy), isMT(c.venueSell)
		if buyMT == sellMT {
			// 双腿皆 CEX（同所/双 CEX）：簿是 mt×cex 仪器天然不覆盖 → B-008 反向对账名单
			uncovered = append(uncovered, name+"（同所组合不管簿）")
			continue
		}
		// mtVenue 不进派生输出：runPairs/backfill 的 MT 侧 venue=exnessVenue env 单一口径
		// （工具现设计）；派生只取 exSym=mtLeg.canonical 供 tick 查询。
		// 腿向关键：MT 腿的 venue 是 MT 域（:mt4/:mt5），CEX 腿 venue 才路由报价源——
		// buy=MT 时 cex=卖侧（venueSell）、卖=MT 时 cex=买侧（venueBuy），禁错取 MT venue。
		var mtCanon, cexCanon, cexVenue string
		if sellMT {
			mtCanon, cexCanon, cexVenue = c.canonicalSell, c.canonicalBuy, c.venueBuy
		} else {
			mtCanon, cexCanon, cexVenue = c.canonicalBuy, c.canonicalSell, c.venueSell
		}
		bare := bareOf(mtCanon)
		sym, ok := cexSymFor(cexVenue, mtCanon, bare)
		if !ok {
			// universe 既无 hedge_canonical 命中也无约定构造命中 → fail-closed 暴露
			uncovered = append(uncovered, name+"（universe 无合约行，簿行缺失）")
			continue
		}
		p := monitorPair{cexVenue: cexVenue, sym: sym, pair: cexCanon + "-" + bare, exSym: mtCanon}
		k := p.cexVenue + "|" + p.pair + "|" + p.exSym
		if !seen[k] {
			seen[k] = true
			pairs = append(pairs, p)
		}
	}
	return pairs, uncovered
}
