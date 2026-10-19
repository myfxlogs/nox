// pairs.go D-423→D-472 S5→D-144：监控对清单派生（硬编码 9 行 vs xasset_allow_combos
// 双真相源漂移实证——okx XPT/XPD 簿缺席告警 9-25 实发——重写为白名单表派生）。
// 覆盖域扩至上游验证域：派生源=enabled 白名单 ∪ active 纸面仓 ∪ fin_carry_universe
// 达标行（cfd 结构 note=” net∈[8,300]，与 openCfdDiscovery 开仓域同口径）——授权域/
// 发现域/在仓域三源并集，白名单缩编簿不塌（10-02→10-04 塌缩事故根修）。
// mt_venue 集=ticks 在流 MT 账户动态枚举（NOX_XASSET_EXNESS_VENUE env 删除；各账户
// 独立采样禁借用——XAUUSD 点差 3001157≈0.24 vs 15032313≈0.15-0.21 实锤失真）。
// 派生键=消费端权威口径 scan.CfdBookSpecs/cfdPairCandidates 同源：
// pair=cexCanonical+"-"+TrimSuffix(mtCanonical,"USD")，class 行经 asset_equiv 实解析。
// 白名单加行/纸面开仓≤5min（cron 轮重查表）簿覆盖自动跟上，零代码触碰。
package main

import (
	"nox/internal/data/keydomain"
	"nox/internal/research/scan"
	"nox/internal/store/pgstore"

	"sort"
	"strings"
)

// monitorPair D-335R1 S1 + D-423 venue 化 + D-144 多账户：{cexVenue, sym, pair, exSym,
// mtVenueKey}——cexVenue 路由 CEX 报价源（binance:perp=经 cex-proxy fapi；okx:perp=OKX
// 公开行情），行级 venue 三元组落库（pair,cex_venue,mt_venue），消费端按仓 venue 直查
// 禁借用；mtVenueKey=该对所属 MT 账户键（"15032313:mt5"），exSym=该账户 ticks 实查键。
type monitorPair struct{ cexVenue, sym, pair, exSym, mtVenueKey string }

// allowComboRow 派生入参：xasset_allow_combos enabled 行投影。
type allowComboRow struct{ canonicalBuy, venueBuy, canonicalSell, venueSell string }

// universeRow 派生入参：fin_cex_universe（status='TRADING'）行投影。
type universeRow struct{ venue, symbol, hedgeCanonical string }

// cfdDemandRow D-144：簿需求行——fin_carry_universe 达标行 ∪ active 纸面仓
// (canonical,mt,cex) 三元组（venue 皆裸名）。
type cfdDemandRow struct{ canonical, mtVenue, cexVenue string }

// mtAcct ticks 在流 MT 账户（10min 新鲜窗枚举；venue 裸账号+vtype+在流 symbol 集——
// demand 簿对 exSym 只取该账户自身采样面，各账户独立禁借用）。
type mtAcct struct {
	venue, vtype string
	syms         []string
}

// legacyPairs D-472 S5：跨资产簿常量保留（PAXG-XAU/XAUT-XAU=金属代币对 XAU 现货价，
// 非消费端 paperCfdPairKey 口径=不可派生行，注释标 legacy；消费方未全审=保守保留）。
// mtVenueKey 由派生按 venue 集展开（各账户独立采样）。
var legacyPairs = []monitorPair{
	{"binance:perp", "PAXGUSDT", "PAXG-XAU", "XAUUSD", ""},
	{"binance:perp", "XAUTUSDT", "XAUT-XAU", "XAUUSD", ""},
	// D-335/D-419 历史观察行（现行 9 行对拍中唯一无 enabled 组合源者；B-009 保守保留，
	// 删行=决策席裁定）。
	{"binance:perp", "BTCUSDT", "BTCUSDT-BTC", "BTCUSDT", ""},
}

// nonDerivablePairs D-472 B-007：跨资产/非加密对显式排除集（fin_cex_universe 无合约行
// =结构性不可派生；列名跳过、不进 miss warn 与反向对账名单——防每轮刷屏）。
// 键=canonicalBuy+"|"+canonicalSell（双向命中）。
var nonDerivablePairs = map[string]bool{
	"CLUSDT|WTI": true, // D-330 demo 组合：原油期货 perp×WTI 现货
}

// deriveMonitorPairs D-472 S5 白名单派生→D-144 三源并集（纯函数，可测）。
// 输入：combos=enabled 行；uni=TRADING 行；demand=universe 达标行∪active 纸面仓
// (canonical,mt,cex)；accts=ticks 在流 MT 账户；equiv=asset_equiv enabled 行；
// caps=账户能力账本（broker_symbol_map 裸户→canonical 集；nil=滤关——测试零值路径，
// 生产 loadDerivationInputs 恒非 nil）。
// 返回派生清单（∪legacy，含 mtVenueKey 去重）+反向对账 uncovered 组合名
// （enabled/demand 行未被派生覆盖者；nonDerivable 命名行除外——结构性已知不刷屏）。
// 组合×venue 展开按 caps 过滤（裁定注记53③/簿饥饿销账遗留②）：无映射=该户无此
// 能力不产对（TRXUSDT@110660304 类结构性 skip 噪音根除）；demand 绑定对不滤
// （仓驱动必须继续暴露）。venue 全被滤的组合入 uncovered（反向对账不静默）。
func deriveMonitorPairs(combos []allowComboRow, uni []universeRow, demand []cfdDemandRow,
	accts []mtAcct, equiv []pgstore.AssetEquivEntry, caps map[string]map[string]bool) (pairs []monitorPair, uncovered []string) {
	byKey := map[string]string{} // venue|hc → symbol
	symsByVenue := map[string]map[string]bool{}
	for _, r := range uni {
		byKey[r.venue+"|"+r.hedgeCanonical] = r.symbol
		if symsByVenue[r.venue] == nil {
			symsByVenue[r.venue] = map[string]bool{}
		}
		symsByVenue[r.venue][r.symbol] = true
	}
	isMT := keydomain.IsMTKey
	bareOf := bareCanon // MT canonical 裸形归一单源（capabilities.go；组合行/map 两侧键域合流点）
	// cexSymFor：universe 命中（venue+hedge_canonical）→ miss 按 venue 约定构造+回查
	// 存在性 → 仍 miss=false（fail-closed 不静默丢，调用方 skip+warn）。
	cexSymFor := func(cexVenueKey, mtCanonical, bare string) (string, bool) {
		v := keydomain.BareOf(cexVenueKey)
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

	// mt venue 集——ticks 在流账户 ∪ enabled 组合 MT 腿（venue 键全集；
	// demand 行 venue 各自绑定不入全集——其 pair 键含账户实解析 canonical 不外溢）。
	acctType := map[string]string{} // bare → vtype
	acctSyms := map[string][]string{}
	for _, a := range accts {
		acctType[a.venue] = a.vtype
		acctSyms[a.venue] = a.syms
	}
	njk := func(s string) string {
		if b := strings.TrimSuffix(s, "USDT"); b != s && b != "" {
			return b
		}
		if b := strings.TrimSuffix(s, "USD"); b != s && b != "" {
			return b
		}
		return s
	}
	// demandExSym demand 行 MT 腿 tick 查询键：账户在流 symbol NJK 命中 canonical
	// 优先（本账户实采样面），退 spec.ExSym（等价类 canonical），最后 canonical 兜底。
	demandExSym := func(mtBare, canonical, specExSym string) string {
		for _, s := range acctSyms[mtBare] {
			if njk(s) == njk(canonical) {
				return s
			}
		}
		if specExSym != "" {
			return specExSym
		}
		return canonical
	}
	mtKeyFor := func(bare string) string {
		if t, ok := acctType[bare]; ok {
			return keydomain.Join(bare, t)
		}
		return keydomain.Join(bare, "mt5") // 与 scan.monitorVenueKeys 缺省口径一致
	}
	globalVenueKeys := map[string]bool{}
	for _, a := range accts {
		globalVenueKeys[keydomain.Join(a.venue, a.vtype)] = true
	}
	for _, c := range combos {
		for _, v := range []string{c.venueBuy, c.venueSell} {
			if isMT(v) {
				globalVenueKeys[v] = true
			}
		}
	}

	pairs = make([]monitorPair, 0, len(legacyPairs)*len(globalVenueKeys)+len(combos))
	seen := map[string]bool{}
	emit := func(p monitorPair) {
		k := p.cexVenue + "|" + p.pair + "|" + p.exSym + "|" + p.mtVenueKey
		if !seen[k] {
			seen[k] = true
			pairs = append(pairs, p)
		}
	}
	// capable venue×MT-canonical 能力判定：有账无 canonical=无能力（fail-closed）；
	// 无账（map 全无该户行）=无能力；caps nil=滤关。键双侧过 bareOf：户号裸名
	// （map.venue 域）+canonical 裸形（1055 后组合行 TRX×map TRXUSDT 同域合流）。
	capable := func(venueKey, mtCanon string) bool {
		if caps == nil {
			return true
		}
		return caps[keydomain.BareOf(venueKey)][bareOf(mtCanon)]
	}
	// 确定性序：venue 键排序后展开（同输入同输出）。
	venues := make([]string, 0, len(globalVenueKeys))
	for v := range globalVenueKeys {
		venues = append(venues, v)
	}
	sort.Strings(venues)
	for _, lp := range legacyPairs {
		for _, v := range venues {
			if !capable(v, lp.exSym) { // legacy MT canonical=exSym（XAUUSD/BTCUSDT 域同 map.canonical）
				continue
			}
			p := lp
			p.mtVenueKey = v
			emit(p)
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
		emitted := 0
		for _, v := range venues {
			if !capable(v, mtCanon) {
				continue
			}
			emit(monitorPair{cexVenue: cexVenue, sym: sym, pair: cexCanon + "-" + bare, exSym: mtCanon, mtVenueKey: v})
			emitted++
		}
		if emitted == 0 {
			// 能力滤全剔：派生面无任何账户具备该 canonical——反向对账暴露不静默
			uncovered = append(uncovered, name+"（无能力 venue，能力滤剔除）")
		}
	}
	// 上游域：universe 达标行∪active 纸面仓——按其自带 (mt,cex) 绑账户派生，
	// pair 键经 scan.CfdBookSpecs 实解析（与 insertCfdPaper 候选键同源）。
	for _, d := range demand {
		name := d.mtVenue + "×" + d.cexVenue + "|" + d.canonical
		cexBare := keydomain.BareOf(d.cexVenue)
		emitted := false
		for _, sp := range scan.CfdBookSpecs(equiv, d.canonical, cexBare, d.mtVenue) {
			nat := scan.CexNativeSym(cexBare, sp.CexCanon)
			if nat == "" || !symsByVenue[cexBare][nat] {
				continue // cex 无报价源约定或无 TRADING 合约行
			}
			emit(monitorPair{cexVenue: keydomain.Join(cexBare, "perp"), sym: nat, pair: sp.Pair,
				exSym: demandExSym(d.mtVenue, d.canonical, sp.ExSym), mtVenueKey: mtKeyFor(d.mtVenue)})
			emitted = true
		}
		if !emitted {
			uncovered = append(uncovered, name+"（簿结构性不可覆盖，簿行缺失）")
		}
	}
	return pairs, uncovered
}
