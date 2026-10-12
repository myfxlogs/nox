// paper_window_eval.go —— D-033①：paper 观测窗行健康评估（只读不写库）。
// fail-closed/stalled 计数复刻 accrue 段 noFactWarn 语义并持久化为报告指标——
// 结算对齐探针直接用 AccruePaperLegs（aok=false=边界/率源/映射缺，零口径分叉）。
package scan

import (
	"context"
	"fmt"
	"sort"
	"time"

	"arb/internal/data/fact"
	"arb/internal/store/pgstore"
)

// PaperWindowStore 自盯评估窄读面（*pgstore.Store 原生实现）。
type PaperWindowStore interface {
	PaperFactSource
	ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error)
	// ListAssetEquiv D-043：等价类映射（class 键行 legs 解析单源）。
	ListAssetEquiv(ctx context.Context) ([]pgstore.AssetEquivEntry, error)
}

// PaperDayBucket 日分桶行（日标签 BJ 口径，报告渲染用）。
type PaperDayBucket struct {
	Day     string
	Rows    int
	Sum     float64
	Reasons []string // 仅退出桶：当日 exit_reason 去重清单
}

// PaperComboStat 组合维度统计（D-056①：venue_a×venue_b×canonical×structure 精确
// 配对——同 venue 对的多品种/多结构行互不串账；Stage 1 组合证据口径：
// 窗内累计在仓天数=各行 [entry,exit|now]∩窗 时长和；AccruedUSD=行累计值合计
// （在仓+窗内退出 realized，与窗净额同口径））。
type PaperComboStat struct {
	VenueA     string
	VenueB     string
	Canonical  string // 行 symbol=universe 规范化 canonical
	Structure  string // 行 structure 原值
	Rows       int
	ActiveDays float64
	AccruedUSD float64
}

// PaperWindowEval paper 观测窗单轮评估结果。
type PaperWindowEval struct {
	ActiveN       int // cohort 中 status='active' 行数
	DemoN         int
	ExitedN       int // exit_ts 落窗内
	NetActive     float64
	NetExited     float64
	WindowNet     float64 // 在仓累计 + 退出 realized（脚本 §4 同口径）
	NotionalUSD   float64 // 在仓行名义合计
	FailClosed    int     // accrue 探针 fail-closed 行数（边界/率源/映射缺）
	Stalled       int     // accrue 水位落后 >2×行结算窗（行窗=max(双腿)）
	ActiveByDay   []PaperDayBucket
	ExitedByDay   []PaperDayBucket
	Combos        []PaperComboStat // 配对维度在仓天数/accrued（Stage 1 组合证据）
	LatestAccrue  *time.Time       // active 行水位最迟者（D-028 在线佐证）
	FirstProbeErr string           // 首个探针错误摘要（fail-closed 行明细告警用）
}

// EvalPaperCohort 评估 cohort 行（rows=PaperWindowCohort 结果，结构过滤由调用方完成）。
// 逐行跑真实 accrue 探针取 aok——与生产 accrue 段同一判定函数，口径零漂移。
func EvalPaperCohort(ctx context.Context, st PaperWindowStore, rows []pgstore.PaperSpotCarry, since, now time.Time) (*PaperWindowEval, error) {
	uniRows, err := st.ListCexUniverse(ctx)
	if err != nil {
		return nil, fmt.Errorf("paper eval: list cex universe: %w", err)
	}
	// ②：等价类视图（读失败 fail-open 退化 legacy 键——eval 为观察面非判定面）。
	var view *assetClassView
	if equiv, eerr := st.ListAssetEquiv(ctx); eerr == nil {
		view = newAssetClassView(equiv)
	}
	cexRaw := cexRawCanonIndex(uniRows, view)
	mtCache := map[string]map[string]string{}
	mtRaw := func(venue, canonical string) string {
		m, ok := mtCache[venue]
		if !ok {
			maps, err := st.ListSymbolMappings(ctx, venue)
			if err != nil {
				m = map[string]string{}
			} else {
				m = mtRawIndex(maps)
			}
			mtCache[venue] = m
		}
		return m[NormalizeJoinKey(canonical)]
	}
	ev := &PaperWindowEval{}
	actDay, exitDay := map[string]*PaperDayBucket{}, map[string]*PaperDayBucket{}
	combos := map[string]*PaperComboStat{}
	combo := func(p *pgstore.PaperSpotCarry) *PaperComboStat {
		k := p.VenueA + "|" + p.VenueB + "|" + p.Symbol + "|" + p.Structure
		c := combos[k]
		if c == nil {
			c = &PaperComboStat{VenueA: p.VenueA, VenueB: p.VenueB, Canonical: p.Symbol, Structure: p.Structure}
			combos[k] = c
		}
		return c
	}
	comboDays := func(p *pgstore.PaperSpotCarry) float64 {
		start := p.EntryTs
		if start.Before(since) {
			start = since
		}
		end := now
		if p.ExitTs != nil && p.ExitTs.Before(now) {
			end = *p.ExitTs
		}
		if d := end.Sub(start); d > 0 {
			return d.Hours() / 24
		}
		return 0
	}
	for _, p := range rows {
		if p.Status == "exited" {
			if p.ExitTs == nil || p.ExitTs.Before(since) { // 窗内退出口径同脚本 §3
				continue
			}
			ev.ExitedN++
			ev.NetExited += p.AccruedUSD
			d := p.ExitTs.In(bjLoc).Format("01-02")
			b := exitDay[d]
			if b == nil {
				b = &PaperDayBucket{Day: d}
				exitDay[d] = b
			}
			b.Rows++
			b.Sum += p.AccruedUSD
			if p.ExitReason != "" && !strIn(b.Reasons, p.ExitReason) {
				b.Reasons = append(b.Reasons, p.ExitReason)
			}
			c := combo(&p)
			c.Rows++
			c.ActiveDays += comboDays(&p)
			c.AccruedUSD += p.AccruedUSD
			continue
		}
		if p.Status == "active" {
			ev.ActiveN++
			ev.NetActive += p.AccruedUSD
			ev.NotionalUSD += p.QtyUSD
			day := "never"
			if p.LastAccrueTs != nil {
				day = p.LastAccrueTs.In(bjLoc).Format("01-02")
				if ev.LatestAccrue == nil || p.LastAccrueTs.After(*ev.LatestAccrue) {
					ev.LatestAccrue = p.LastAccrueTs
				}
			}
			b := actDay[day]
			if b == nil {
				b = &PaperDayBucket{Day: day}
				actDay[day] = b
			}
			b.Rows++
			b.Sum += p.AccruedUSD
			c := combo(&p)
			c.Rows++
			c.ActiveDays += comboDays(&p)
			c.AccruedUSD += p.AccruedUSD
		} else if p.Status == "demo" {
			ev.DemoN++
		}
		if p.Status != "active" && p.Status != "demo" {
			continue
		}
		fc, stall, perr := evalPaperRow(ctx, st, &p, cexRaw, mtRaw, view, now)
		if perr != nil && ev.FirstProbeErr == "" {
			ev.FirstProbeErr = perr.Error()
		}
		if fc {
			ev.FailClosed++
		}
		if stall {
			ev.Stalled++
		}
	}
	ev.WindowNet = ev.NetActive + ev.NetExited
	ev.ActiveByDay = sortedBuckets(actDay)
	ev.ExitedByDay = sortedBuckets(exitDay)
	for _, c := range combos {
		ev.Combos = append(ev.Combos, *c)
	}
	sort.Slice(ev.Combos, func(i, j int) bool {
		a, b := ev.Combos[i], ev.Combos[j]
		return a.VenueA+a.VenueB+a.Canonical+a.Structure < b.VenueA+b.VenueB+b.Canonical+b.Structure
	})
	return ev, nil
}

// evalPaperRow 单行健康判定：fc=accrue 探针 fail-closed（含腿映射缺）；
// stall=水位落后 >2×行结算窗（行窗=max（双腿间隔），MT 腿恒 24h 日界）。
func evalPaperRow(ctx context.Context, st PaperWindowStore, p *pgstore.PaperSpotCarry,
	cexRaw map[string]string, mtRaw func(string, string) string, view *assetClassView,
	now time.Time) (fc, stall bool, err error) {
	cexV, mtV := p.VenueB, p.VenueA // D-498：cfd 行 venue_a=MT 臂、venue_b=CEX 臂
	rawCex := cexRaw[cexV+"|"+NormalizeJoinKey(p.Symbol)]
	rawMt := mtRaw(mtV, p.Symbol)
	if rawMt == "" {
		rawMt = mtRaw(mtV, view.mtCanon(p.Symbol, mtV)) // D-043 ②：class 键行→asset_equiv 解析
	}
	if rawCex == "" || rawMt == "" {
		return true, false, nil // canonical→raw 映射缺 → 边界事实键无着落，fail-closed
	}
	swapKind, sideFactor := fact.KindSwapLong, 1.0
	if p.Side == "long_perp" {
		swapKind, sideFactor = fact.KindSwapShort, -1.0
	}
	facts, ferr := st.LatestFacts(ctx, fact.KindFunding, cexV, rawCex)
	if ferr != nil {
		return false, false, fmt.Errorf("funding %s|%s: %w", cexV, rawCex, ferr)
	}
	cur, curOK := 0.0, len(facts) > 0
	if curOK {
		cur = facts[0].Value
	}
	legs := []PaperLegAccrue{
		{Kind: fact.KindFunding, Venue: cexV, Symbol: rawCex, SideFactor: sideFactor, CurRate: cur, CurOK: curOK},
		{Kind: swapKind, Venue: mtV, Symbol: rawMt, SideFactor: 1},
	}
	base := p.EntryTs
	if p.LastAccrueTs != nil {
		base = *p.LastAccrueTs
	}
	_, _, aok, aerr := AccruePaperLegs(ctx, st, legs, base, now, p.QtyUSD)
	if aerr != nil {
		return false, false, fmt.Errorf("计息探针 行%d: %w", p.ID, aerr) // D-056②：串入日报正文——禁内部词
	}
	if !aok {
		return true, false, nil
	}
	// 行结算窗=max(双腿)：funding 腿 iv（facts 实读，缺→不判 stall 该项已 aok 覆盖）
	// 与 MT 腿 UTC 日界 24h（paperRowSettleWindow 单实现，accrue 告警阈同口径）。
	return false, now.Sub(base) > 2*paperRowSettleWindow(ctx, st, cexV, rawCex), nil
}

// sortedBuckets 分桶转序列表（日标签字典序=MM-DD 日历序）。
func sortedBuckets(m map[string]*PaperDayBucket) []PaperDayBucket {
	out := make([]PaperDayBucket, 0, len(m))
	for _, b := range m {
		out = append(out, *b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Day < out[j].Day })
	return out
}

func strIn(ss []string, s string) bool {
	for _, v := range ss {
		if v == s {
			return true
		}
	}
	return false
}

// bjLoc 北京时区（报表日分桶口径）。
var bjLoc = func() *time.Location {
	l, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		return time.FixedZone("CST", 8*3600)
	}
	return l
}()
