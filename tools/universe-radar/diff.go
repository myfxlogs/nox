package main

// D-307 S4：归一/分诊/diff 纯函数层（不碰 IO，注入假 rows+假腿集可单测）。
//
// 事件语义（设计 §4）：
//   - NEW / DELISTED / RELISTED / NEW_CLASS / HEDGE_APPEARED = 一次性转移事件（落事件+告警）；
//   - FUNDING 类提示 = |ann|≥阈值且 vol≥阈值 → 走 last_alert_ts 24h 冷却，不落事件；
//   - FUNDING_DIVERGENCE = 同 canonical 跨所 |ann_i−ann_j|≥阈值 且两侧 vol≥阈值 →
//     事件行即告警记录，先查 LastCexEventTS ≥24h 才落（冷却内零写入，调用方在 main.go 查）。
//   - 旧 universe 为空 = 首轮 bootstrap：只 upsert+radar 落库，零事件零告警（防 ~2200 NEW 洪泛）。

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"arb/internal/store/pgstore"
)

const statusTrading = "TRADING" // 归一后「在交易中」统一值（sources.go 各所适配）

// alias base→canonical 别名表（设计附录 A，初版静态 map；追加属配置活）。
var alias = map[string]string{
	"XAU": "XAUUSD", "XAG": "XAGUSD", "XPT": "XPTUSD", "XPD": "XPDUSD",
	"COPPER": "XCUUSD", "CL": "WTI", "BZ": "BRENT", "NATGAS": "NATGAS",
}

// normBase 剥报价后缀得 base：XAUUSDT→XAU、XAU_USDT→XAU、XAU-USDT-SWAP→XAU。
func normBase(symbol string) string {
	for _, suf := range []string{"-USDT-SWAP", "_USDT", "USDT"} {
		if strings.HasSuffix(symbol, suf) {
			return strings.TrimSuffix(symbol, suf)
		}
	}
	return symbol
}

// canonicalOf base→canonical：别名表命中→映射值；未命中→base 本身（加密货币照常）。
func canonicalOf(base string) string {
	if c, ok := alias[base]; ok {
		return c
	}
	return base
}

// annualize funding 年化 %：rate × (24/interval_h) × 365 × 100；interval 逐符号真值，
// 禁默认 8h（D-301a 同族）。intervalH<=0=无数据 → nil。
func annualize(rate float64, intervalH int) *float64 {
	if intervalH <= 0 {
		return nil
	}
	v := rate * (24.0 / float64(intervalH)) * 365 * 100
	return &v
}

// normRow 归一化后的行（diff/tier/落库共用）。
type normRow struct {
	Row
	Venue          string
	Canonical      string
	Tier           string
	HedgeCanonical string // join 实际命中的腿 canonical，空=无腿
	HedgeClass     string // 命中经 asset_equiv 时的资产类（如 GOLD），broker 腿命中为空
	FundingAnn     *float64
}

// hedgeHit 对冲腿判定：canonical ∈ broker_symbol_map ∪ asset_equiv.canonical，或
// symbol 本身 ∈ asset_equiv.canonical（如 PAXGUSDT 的对冲腿=同 class 的 XAUUSD）。
// 返回实际命中的成员键（存 hedge_canonical——命什么存什么，不存 normBase 幻影值）。
func hedgeHit(r Row, canonical string, hedgeSet map[string]bool) (string, bool) {
	if hedgeSet[canonical] {
		return canonical, true
	}
	if hedgeSet[r.Symbol] {
		return r.Symbol, true
	}
	return "", false
}

// tierOf 三档分诊：有腿→T1；无腿且（Class 非空且≠COIN ∨ base 命中别名表即非加密外观）→T2；其余→T3。
func tierOf(r Row, hedge bool) string {
	if hedge {
		return "T1"
	}
	_, aliasHit := alias[normBase(r.Symbol)]
	if (r.Class != "" && r.Class != "COIN") || aliasHit {
		return "T2"
	}
	return "T3"
}

// normalize 三所原始行 → 归一行（base/canonical/tier/funding_ann 一次算齐）。
// classOf=asset_equiv canonical→asset_class（broker_symbol_map 腿无类，告警行展示用）。
func normalize(venue string, rows []Row, hedgeSet map[string]bool, classOf map[string]string) []normRow {
	out := make([]normRow, 0, len(rows))
	for _, r := range rows {
		canon := canonicalOf(normBase(r.Symbol))
		hit, hedge := hedgeHit(r, canon, hedgeSet)
		nr := normRow{Row: r, Venue: venue, Canonical: canon,
			Tier:           tierOf(r, hedge),
			FundingAnn:     annualize(r.FundingRate, r.IntervalH),
			HedgeCanonical: hit, HedgeClass: classOf[hit]}
		out = append(out, nr)
	}
	return out
}

// diffUniverse 旧 universe map vs 新归一行 → 转移事件列表。
// 缺席但旧 status 仍 TRADING 的不判退市（API 侧可能过滤暂停盘，保守只认 status 字段）。
func diffUniverse(old map[string]pgstore.CexUniverseRow, rows []normRow) []pgstore.CexEvent {
	if len(old) == 0 {
		return nil // 首轮 bootstrap 静默
	}
	oldClasses := make(map[string]bool)
	for _, o := range old {
		if o.Class != "" {
			oldClasses[o.Class] = true
		}
	}
	var evs []pgstore.CexEvent
	seenNewClass := make(map[string]bool) // 同类多新符号一轮只报一次 NEW_CLASS
	for _, n := range rows {
		o, ok := old[n.Venue+"|"+n.Symbol]
		if !ok {
			evs = append(evs, pgstore.CexEvent{Venue: n.Venue, Symbol: n.Symbol, EventType: "NEW",
				Detail: fmt.Sprintf("tier=%s class=%s hedge=%s", n.Tier, n.Class, n.HedgeCanonical)})
		} else {
			if o.Status == statusTrading && n.Status != statusTrading {
				evs = append(evs, pgstore.CexEvent{Venue: n.Venue, Symbol: n.Symbol, EventType: "DELISTED",
					Detail: fmt.Sprintf("status %s→%s", o.Status, n.Status)})
			} else if o.Status != statusTrading && n.Status == statusTrading {
				evs = append(evs, pgstore.CexEvent{Venue: n.Venue, Symbol: n.Symbol, EventType: "RELISTED",
					Detail: fmt.Sprintf("status %s→%s", o.Status, n.Status)})
			}
			if (o.Tier == "T2" || o.Tier == "T3") && n.Tier == "T1" {
				evs = append(evs, pgstore.CexEvent{Venue: n.Venue, Symbol: n.Symbol, EventType: "HEDGE_APPEARED",
					Detail: fmt.Sprintf("tier %s→T1 hedge=%s", o.Tier, n.HedgeCanonical)})
			}
		}
		if n.Class != "" && !oldClasses[n.Class] && !seenNewClass[n.Class] {
			seenNewClass[n.Class] = true
			evs = append(evs, pgstore.CexEvent{Venue: n.Venue, Symbol: n.Symbol, EventType: "NEW_CLASS",
				Detail: "underlying_class=" + n.Class})
		}
	}
	return evs
}

// fundingEventKind D-353 S1：funding 告警事件类型（事件语义——只在状态转移时发声，
// 稳态零报；24h last_alert_ts 冷却保留作 flap 抑制）。
type fundingEventKind string

const (
	fundingNewEntry fundingEventKind = "new_entry" // 本轮新进极端带
	fundingSignFlip fundingEventKind = "sign_flip" // 符号翻转
	fundingExitBand fundingEventKind = "exit_band" // 跌出极端带（行仍在）
)

// fundingEvent funding 转移事件：kind 标签 + 行 + 旧值（文案含旧→新两侧数值）。
type fundingEvent struct {
	row    normRow
	kind   fundingEventKind
	oldAnn *float64 // 转移前 ann（old 缺行/nil → nil）
}

// fundingQualify 极端带判定：|ann|≥pct ∧ vol≥volMin（ann 非 nil）。
func fundingQualify(n normRow, fundingMinPct, volMinUSD float64) bool {
	return n.FundingAnn != nil && math.Abs(*n.FundingAnn) >= fundingMinPct && n.Vol24h >= volMinUSD
}

// fundingTransitions D-353 S1：funding 告警事件化——只在状态转移时发声，稳态零报。
//   - new_entry：本轮 qualify ∧（old 无行 ∨ old 不 qualify）∧ 非翻转；
//   - sign_flip：本轮 qualify ∧ old qualify ∧ 符号相反；
//   - exit_band：old qualify ∧ 本轮 qualify 失效（ann 归常态/归 nil/vol 跌破）——
//     行整体消失不在此列（循环只遍历本轮行；DELISTED 转移事件已单报，防双报）。
//
// 冷却与发送仍在 main.go（last_alert_ts 24h=flap 抑制，进出带抖动至多一报/日）。
func fundingTransitions(rows []normRow, old map[string]pgstore.CexUniverseRow, fundingMinPct, volMinUSD float64) []fundingEvent {
	var out []fundingEvent
	for _, n := range rows {
		key := n.Venue + "|" + n.Symbol
		var oldAnn *float64
		oldQualify := false
		if o, had := old[key]; had {
			oldAnn = o.FundingAnn
			oldQualify = o.FundingAnn != nil && o.Vol24h != nil &&
				math.Abs(*o.FundingAnn) >= fundingMinPct && *o.Vol24h >= volMinUSD
		}
		qual := fundingQualify(n, fundingMinPct, volMinUSD)
		switch {
		case qual && oldQualify:
			if (*oldAnn >= 0) != (*n.FundingAnn >= 0) {
				out = append(out, fundingEvent{row: n, kind: fundingSignFlip, oldAnn: oldAnn})
			}
			// 同号稳态：零事件（每日重报根因即此处退化）
		case qual:
			out = append(out, fundingEvent{row: n, kind: fundingNewEntry, oldAnn: oldAnn})
		case oldQualify:
			out = append(out, fundingEvent{row: n, kind: fundingExitBand, oldAnn: oldAnn})
		}
	}
	return out
}

// classFilteredKinds D-374 A 面：股权/指数/盘前类 perp 的 funding 语义各所不同（休市冻结/
// 基准不同），跨所数值差不可解释也不可执行 → divergence 压 digest（保留可见性不删）。
// 豁免：COMMODITY（XAU 类 CEX perp funding 24/7 真有价）/FX/COIN/空 class（不能证明是股票）。
var classFilteredKinds = map[string]bool{
	"EQUITY": true, "CN_EQUITY": true, "HK_EQUITY": true, "KR_EQUITY": true,
	"INDEX": true, "PREMARKET": true,
}

// divergences 同 canonical 跨所 funding 分化候选：|ann_i−ann_j|≥divPct 且两侧 vol≥volMinUSD。
// venue='multi'/symbol=canonical/detail=所对+两侧年化；24h 冷却由调用方查 LastCexEventTS。
// D-374 A 面：canonical 分组内任一侧非空 Class ∈ classFilteredKinds → 全部 pair 跳过，
// canonical 计入 classFiltered 返回（digest 汇总渲染）；双侧矛盾取非空侧=宁可信其有。
func divergences(rows []normRow, divPct, volMinUSD float64) (evs []pgstore.CexEvent, classFiltered []string) {
	byCanon := make(map[string][]normRow)
	for _, n := range rows {
		if n.FundingAnn != nil && n.Vol24h >= volMinUSD {
			byCanon[n.Canonical] = append(byCanon[n.Canonical], n)
		}
	}
	for canon, rs := range byCanon {
		filtered := false
		for _, r := range rs {
			if classFilteredKinds[r.Class] {
				filtered = true
				break
			}
		}
		if filtered {
			classFiltered = append(classFiltered, canon)
			continue
		}
		for i := 0; i < len(rs); i++ {
			for j := i + 1; j < len(rs); j++ {
				a, b := rs[i], rs[j]
				if a.Venue == b.Venue {
					continue
				}
				if b.Venue < a.Venue { // 所名排序保证 detail 输出确定
					a, b = b, a
				}
				if math.Abs(*a.FundingAnn-*b.FundingAnn) >= divPct {
					evs = append(evs, pgstore.CexEvent{
						Venue: "multi", Symbol: canon, EventType: "FUNDING_DIVERGENCE",
						Detail: fmt.Sprintf("%s:%s %+.1f%% vs %+.1f%% ann", a.Venue, b.Venue, *a.FundingAnn, *b.FundingAnn),
					})
				}
			}
		}
	}
	sort.Strings(classFiltered) // map 迭代无序 → digest 输出确定
	return evs, classFiltered
}

// cooldownOK last_alert_ts 冷却判定：nil/超过 24h → 可告警。
func cooldownOK(last *time.Time, now time.Time) bool {
	return last == nil || now.Sub(*last) >= 24*time.Hour
}

// basisBps D-354 S1：基差=(mark/index−1)×1e4，round 1 位；mark≤0 或 index≤0 → nil
// （fail-closed，不臆造；okx 行无 index 面→NULL 诚实缺）。
func basisBps(mark, index float64) *float64 {
	if mark <= 0 || index <= 0 {
		return nil
	}
	v := math.Round((mark/index-1)*1e4*10) / 10
	return &v
}
