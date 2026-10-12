// classify.go —— D-170：MT 腿静默归因裁决器（四源鉴别：闭市/venue 死/券商禁交易或
// 冻结报价/真单腿断流）。纯裁决无发射——三发射点在告警插入前过闸，
// closed→不发（D-168 episode 连开都不开），unknown→保 warn 原径（fail-closed 不误赦）。
package marketstate

import (
	"context"
	"strings"
	"time"

	"arb/internal/bus"
	"arb/internal/fact"
	"arb/internal/keydomain"
	"arb/internal/store/pgstore"
)

// Verdict 静默归因。
type Verdict string

const (
	Closed           Verdict = "closed"             // session 闭市窗（含假日窗）
	VenueDead        Verdict = "venue_dead"         // 开市+全 venue 符号静默
	BrokerDisabled   Verdict = "broker_disabled"    // trade_mode 近窗翻 Disabled/CloseOnly
	LegDeadBroker    Verdict = "leg_dead_broker"    // 同 canonical 他 venue 在走，本腿独静
	LegFrozenSuspect Verdict = "leg_frozen_suspect" // 本腿静+参照腿高速动（冻结报价疑似，最重档）
	MarketWideQuiet  Verdict = "market_wide_quiet"  // canonical ≥2 venue 承载且全静
	Unknown          Verdict = "unknown"            // 证据缺（fail-closed 保 warn 不降级）
	NotApplicable    Verdict = "not_applicable"     // CEX 腿无 session 概念
)

// Result 裁决结果（Level="" 表示不告警——仅 closed；其余按表定级）。
type Result struct {
	Verdict Verdict
	Level   string // info/warn/critical；closed=""(不告警)
	Detail  string // 归因短句供告警/拒单文案
}

// Deps 裁决数据面（*pgstore.Store 天然实现；测试可 mock）。
type Deps interface {
	SessionSource
	FactsSince(ctx context.Context, kind, venue, symbol string, since time.Time) ([]fact.Fact, error)
	TicksPriceChanged(ctx context.Context, venue, symbol string, window time.Duration) (changed bool, n int, err error)
	VenueTickLatestAt(ctx context.Context, venue string) (time.Time, bool, error)
	VenuesByCanonical(ctx context.Context, canonical string) ([]string, error)
	MacroEventsBetween(ctx context.Context, from, to time.Time) ([]pgstore.MacroEvent, error)
}

// 裁决参数（包级 var 测试可调；生产缺省）。
var (
	venueActivityWindow = 5 * time.Minute  // venue 活性窗：ticks 最新帧超龄=全 venue 静
	legReferenceWindow  = 30 * time.Minute // 跨 venue 同 canonical 参照窗（对齐 legTickWindow 缺省）
	tradeModeFlipSince  = 24 * time.Hour   // trade_mode 翻转回看窗
	frozenSuspectN      = 20               // 参照窗 DISTINCT bid ≥N=高速动
	holidayCorroborate  = 24 * time.Hour   // macro holiday 佐证半径
)

// tradeModeRecentlyDisabled 近窗内 trade_mode 翻 Disabled/CloseOnly（facts ts 升序：
// 末行=最新须为死值，且窗内存在更早活值行=由活转死翻转实证；窗内只见死值=无证据不判）。
func tradeModeRecentlyDisabled(facts []fact.Fact) bool {
	if len(facts) == 0 {
		return false
	}
	last := int64(facts[len(facts)-1].Value)
	if last != fact.TradeModeDisabled && last != fact.TradeModeCloseOnly {
		return false
	}
	for _, f := range facts[:len(facts)-1] {
		if v := int64(f.Value); v != fact.TradeModeDisabled && v != fact.TradeModeCloseOnly {
			return true
		}
	}
	return false
}

// ClassifyLegSilence 腿级裁决：venue=bare 或复合键皆可（内部 BareOf 归一），
// canonical=ticks/session 键域的 canonical 名。
func ClassifyLegSilence(ctx context.Context, st Deps, venue, canonical string, now time.Time) Result {
	bare := keydomain.BareOf(venue)
	// 0. CEX 腿无 session 概念——跳过（票面禁区）。
	if keydomain.Parse(venue).IsCex() || bus.IsCexVenueKey(venue) {
		return Result{Verdict: NotApplicable, Detail: "cex 腿无 session 裁决"}
	}
	table := LoadSessionTable(ctx, st)

	// 1. session 闭市窗 → closed（休市含假日，静默=预期）。
	open, known := table.OpenAt(bare, canonical, now)
	if known && !open {
		return Result{Verdict: Closed, Detail: "session 闭市窗"}
	}

	// 2. venue 活性：表缺席时 venue 级静默也可判 venue_dead?——否：session 缺席
	// 无法证明「开市」，全 venue 静可能是闭市期正常态 → 需开市证据才判 venue_dead。
	// （venue 有任一符号开市窗即可作为开市证据——venue 级语义同 VenueAllClosed。）
	venueLive, venueOK := false, false
	if latest, ok, err := st.VenueTickLatestAt(ctx, bare); err == nil && ok {
		venueOK = true
		venueLive = now.Sub(latest) <= venueActivityWindow
	}
	if !venueLive {
		// 开市证据：本腿 session 开市，或 venue 任一符号开市窗（venue 级死但本腿表缺席仍可判死）。
		openVenue := open && known
		if !openVenue {
			for k := range table {
				v, _, ok2 := keyCut(k)
				if !ok2 || v != bare {
					continue
				}
				if o, _ := table.OpenAt(bare, k[len(v)+1:], now); o {
					openVenue = true
					break
				}
			}
		}
		if openVenue && venueOK {
			return Result{Verdict: VenueDead, Level: "warn", Detail: "开市窗内全 venue 静默"}
		}
		return Result{Verdict: Unknown, Level: "warn", Detail: "全 venue 静且开市证据缺（表缺席/无帧）"}
	}

	// 3. venue 活+本腿静 → trade_mode 近窗翻 Disabled/CloseOnly → broker_disabled
	// （session 开或缺皆查——venue 活已证市场面有流，翻转证据独立于 session）。
	if fs, err := st.FactsSince(ctx, fact.KindTradeMode, bare, canonical, now.Add(-tradeModeFlipSince)); err == nil && tradeModeRecentlyDisabled(fs) {
		return Result{Verdict: BrokerDisabled, Level: "warn", Detail: "trade_mode 近窗翻 Disabled/CloseOnly（券商禁交易）"}
	}

	// 4. 跨 venue 参照：同 canonical 他 venue 在走 → 本腿独死；
	//    参照腿窗内 DISTINCT bid ≥frozenSuspectN=高速动 → leg_frozen_suspect 升 critical。
	venues, _ := st.VenuesByCanonical(ctx, canonical)
	moving, refN := false, 0
	for _, ov := range venues {
		if ov == bare {
			continue
		}
		if ch, n, err := st.TicksPriceChanged(ctx, ov, canonical, legReferenceWindow); err == nil && ch {
			moving = true
			if n > refN {
				refN = n
			}
		}
	}
	if moving {
		if refN >= frozenSuspectN {
			return Result{Verdict: LegFrozenSuspect, Level: "critical", Detail: "本腿静+参照 venue 高速动（疑似冻结报价）"}
		}
		return Result{Verdict: LegDeadBroker, Level: "warn", Detail: "同 canonical 他 venue 在走，本腿独静"}
	}

	// 5. canonical ≥2 venue 承载且全静 → market_wide_quiet（独苗不适用——
	// 独死归 leg_dead_broker 不误赦成市场安静）；holiday 佐证→info 否则 warn。
	if len(venues) >= 2 {
		lvl := "warn"
		if evs, err := st.MacroEventsBetween(ctx, now.Add(-holidayCorroborate), now.Add(holidayCorroborate)); err == nil {
			for _, e := range evs {
				if e.Importance == "holiday" {
					lvl = "info"
					break
				}
			}
		}
		return Result{Verdict: MarketWideQuiet, Level: lvl, Detail: "canonical 全承载 venue 同静"}
	}
	if len(venues) == 1 && venues[0] == bare {
		return Result{Verdict: LegDeadBroker, Level: "warn", Detail: "独苗承载无参照系，本腿独静"}
	}
	return Result{Verdict: Unknown, Level: "warn", Detail: "证据缺（venue 活、无参照、无翻转、无承载行）"}
}

// keyCut 拆 venue|symbol 键（session Table 键形）。
func keyCut(k string) (venue, symbol string, ok bool) {
	v, s, found := strings.Cut(k, "|")
	return v, s, found
}

// VenueAllClosed venue 级静默判：该 venue 任一（session 表内）符号开市窗 → 非静默。
// known=false=session 表无该 venue 行（表缺席，调用方回退粗窗/原径兜底）。
func VenueAllClosed(ctx context.Context, st SessionSource, venue string, now time.Time) (closed, known bool) {
	bare := keydomain.BareOf(venue)
	table := LoadSessionTable(ctx, st)
	seen := false
	for k, win := range table {
		v, _, ok := keyCut(k)
		if !ok || v != bare {
			continue
		}
		seen = true
		for _, w := range win {
			if w.InSession(now) {
				return false, true
			}
		}
	}
	return seen, seen // 有行且全市窗→closed；零行→known=false
}
