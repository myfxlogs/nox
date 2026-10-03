// paper_window_eval.go —— D-033①：paper 观测窗行健康评估（只读不写库）。
// fail-closed/stalled 计数复刻 accrue 段 noFactWarn 语义并持久化为报告指标——
// 结算对齐探针直接用 AccruePaperLegs（aok=false=边界/率源/映射缺，零口径分叉）。
package scan

import (
	"context"
	"fmt"
	"sort"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// PaperWindowStore 自盯评估窄读面（*pgstore.Store 原生实现）。
type PaperWindowStore interface {
	PaperFactSource
	ListCexUniverse(ctx context.Context) (map[string]pgstore.CexUniverseRow, error)
}

// PaperDayBucket 日分桶行（日标签 BJ 口径，报告渲染用）。
type PaperDayBucket struct {
	Day     string
	Rows    int
	Sum     float64
	Reasons []string // 仅退出桶：当日 exit_reason 去重清单
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
	LatestAccrue  *time.Time // active 行水位最迟者（D-028 在线佐证）
	FirstProbeErr string     // 首个探针错误摘要（fail-closed 行明细告警用）
}

// EvalPaperCohort 评估 cohort 行（rows=PaperWindowCohort 结果，结构过滤由调用方完成）。
// 逐行跑真实 accrue 探针取 aok——与生产 accrue 段同一判定函数，口径零漂移。
func EvalPaperCohort(ctx context.Context, st PaperWindowStore, rows []pgstore.PaperSpotCarry, since, now time.Time) (*PaperWindowEval, error) {
	uniRows, err := st.ListCexUniverse(ctx)
	if err != nil {
		return nil, fmt.Errorf("paper eval: list cex universe: %w", err)
	}
	cexRaw := cexRawCanonIndex(uniRows)
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
		} else if p.Status == "demo" {
			ev.DemoN++
		}
		if p.Status != "active" && p.Status != "demo" {
			continue
		}
		fc, stall, perr := evalPaperRow(ctx, st, &p, cexRaw, mtRaw, now)
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
	return ev, nil
}

// evalPaperRow 单行健康判定：fc=accrue 探针 fail-closed（含腿映射缺）；
// stall=水位落后 >2×行结算窗（行窗=max（双腿间隔），MT 腿恒 24h 日界）。
func evalPaperRow(ctx context.Context, st PaperWindowStore, p *pgstore.PaperSpotCarry,
	cexRaw map[string]string, mtRaw func(string, string) string, now time.Time) (fc, stall bool, err error) {
	cexV, mtV := p.VenueB, p.VenueA // D-498：cfd 行 venue_a=MT 臂、venue_b=CEX 臂
	rawCex := cexRaw[cexV+"|"+NormalizeJoinKey(p.Symbol)]
	rawMt := mtRaw(mtV, p.Symbol)
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
		return false, false, fmt.Errorf("accrue probe %d: %w", p.ID, aerr)
	}
	if !aok {
		return true, false, nil
	}
	// 行结算窗=max(双腿)：funding 腿 iv（facts 实读，缺→不判 stall 该项已 aok 覆盖）
	// 与 MT 腿 UTC 日界 24h。
	iv := 24 * time.Hour
	if ivF, ierr := st.LatestFacts(ctx, fact.KindFundingInterval, cexV, rawCex); ierr == nil && len(ivF) > 0 && ivF[0].Value > 0 {
		if fiv := time.Duration(int64(ivF[0].Value)) * time.Second; fiv > iv {
			iv = fiv
		}
	}
	return false, now.Sub(base) > 2*iv, nil
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
