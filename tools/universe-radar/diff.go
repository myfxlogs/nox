package main

// 归一/分诊/diff 纯函数层（不碰 IO，注入假 rows+假腿集可单测）。
//
// 事件语义（设计 §4）：
//   - NEW / DELISTED / RELISTED / NEW_CLASS / HEDGE_APPEARED = 一次性转移事件（落事件+告警）；
//   - FUNDING 类提示 = |ann|≥阈值且 vol≥阈值 → 走 last_alert_ts 24h 冷却，不落事件；
//   - FUNDING_DIVERGENCE = 同 canonical 跨所 |ann_i−ann_j|≥阈值 且两侧 vol≥阈值 →
//     事件行即告警记录，先查 LastCexEventTS ≥24h 才落（冷却内零写入，调用方在 main.go 查）。
//   - 旧 universe 为空 = 首轮 bootstrap：只 upsert+radar 落库，零事件零告警（防 ~2200 NEW 洪泛）。
//
// 拆分子件：diff_norm.go（符号归一/别名/年化）、diff_funding.go（funding 转移事件+冷却）、
// diff_divergence.go（跨所分化对）、diff_basis.go（基差轴）。

import (
	"fmt"

	"nox/internal/store/pgstore"
)

const statusTrading = "TRADING" // 归一后「在交易中」统一值（sources.go 各所适配）

// 仪器轴（fin_cex_universe.contract_type 域值，迁移 1018）。
const (
	ctPerp    = "PERP"    // 永续——funding 年化轴
	ctFutures = "FUTURES" // 交割——到期收敛基差年化轴（无 funding 概念）
)

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
		if r.ContractType == "" {
			r.ContractType = ctPerp // 存量语义兜底（源适配显式赋值后此分支不该触发）
		}
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
	oldHasFutures := false
	for _, o := range old {
		if o.Class != "" {
			oldClasses[o.Class] = true
		}
		if o.ContractType == ctFutures {
			oldHasFutures = true
		}
	}
	var evs []pgstore.CexEvent
	seenNewClass := make(map[string]bool) // 同类多新符号一轮只报一次 NEW_CLASS
	for _, n := range rows {
		o, ok := old[n.Venue+"|"+n.Symbol]
		if !ok {
			// 交割轴首扫静默：旧 universe 全 perp（轴尚未存在）时 FUTURES 新行视同
			// bootstrap——批量 NEW 不落事件不告警（同首轮静默语义），行照常 upsert。
			if n.ContractType == ctFutures && !oldHasFutures {
				continue
			}
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
