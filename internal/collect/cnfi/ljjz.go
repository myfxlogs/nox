// ljjz.go — D-247 P1：LJJZ 口径数学与断点检测（唯一数学定义；规格=D-247 施工提示词 §4/S3）。
package cnfi

import (
	"context"
	"fmt"
	"log"
	"time"
)

// LJJZPoint is one dated accumulated-NAV point (non-nil ljjz only, ascending).
type LJJZPoint struct {
	Date string
	LJJZ float64
}

// IntervalReturn returns (LJJZ_end − LJJZ_start)/LJJZ_start × 100 over the given
// ascending series (endpoints = earliest/latest non-null LJJZ days in the slice).
// LJJZ 累计口径天然覆盖分红（DWJZ 除息不影响 LJJZ）。空/单点序列 → nil。
func IntervalReturn(pts []LJJZPoint) *float64 {
	if len(pts) < 2 {
		return nil
	}
	start, end := pts[0].LJJZ, pts[len(pts)-1].LJJZ
	if start <= 0 {
		return nil
	}
	v := (end - start) / start * 100
	return &v
}

// MaxDrawdown returns max(1 − LJJZ_t / running_max) × 100 over the ascending series.
// 单点/空 → nil。
func MaxDrawdown(pts []LJJZPoint) *float64 {
	if len(pts) < 2 {
		return nil
	}
	peak := pts[0].LJJZ
	mdd := 0.0
	for _, p := range pts {
		if p.LJJZ > peak {
			peak = p.LJJZ
		}
		if peak > 0 {
			if dd := (1 - p.LJJZ/peak) * 100; dd > mdd {
				mdd = dd
			}
		}
	}
	return &mdd
}

// DetectCaliberBreak D-247 S3/R1：LJJZ 口径断点检测——相邻非空点 |Δ|/prev > 0.15
// 视为转型/折算断点，返回最早断点日；平滑序列 → nil。首点不判（无前值）。
func DetectCaliberBreak(pts []LJJZPoint) *time.Time {
	var earliest *time.Time
	for i := 1; i < len(pts); i++ {
		prev := pts[i-1].LJJZ
		if prev <= 0 {
			continue
		}
		if chg := (pts[i].LJJZ - prev) / prev; chg > 0.15 || chg < -0.15 {
			day, err := time.Parse("2006-01-02", pts[i].Date)
			if err != nil {
				continue
			}
			if earliest == nil || day.Before(*earliest) {
				d := day
				earliest = &d
			}
		}
	}
	return earliest
}

// BackfillCandidates D-247 S3：候选池净值历史回填（lsjz 分页翻史，每基金 ≥minEntries 条
// 或源尽；每页礼貌间隔；逐基金回填后做断点检测并写 caliber_break_date）。
// 幂等：Upsert ON CONFLICT 语义，重复跑无副作用。
func (c *NAVCollector) BackfillCandidates(ctx context.Context, minEntries int) (map[string]int, error) {
	funds, err := c.store.ListCNFICandidates(ctx)
	if err != nil {
		return nil, err
	}
	perFund := map[string]int{}
	for _, f := range funds {
		n, berr := c.backfillOne(ctx, f.Code, minEntries)
		perFund[f.Code] = n
		if berr != nil {
			log.Printf("cnfi: backfill %s: %v（继续下一只）", f.Code, berr)
			continue
		}
		time.Sleep(1 * time.Second)
	}
	return perFund, nil
}

// backfillOne 翻史单基金：lsjz 分页（pageSize=20，≤40 页=800 条上限≈3 年，R4 按条数不按自然窗）。
// 完成后按全序列做 R1 断点检测（取最早断点日写 caliber_break_date）。
func (c *NAVCollector) backfillOne(ctx context.Context, code string, minEntries int) (int, error) {
	total := 0
	var firstErr error
	for page := 1; page <= 40; page++ {
		url := fmt.Sprintf("%s?fundCode=%s&pageIndex=%d&pageSize=20", c.navBase, code, page)
		raw, err := c.fetch(ctx, url)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		entries, err := parseNAVBody(raw)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			break
		}
		if len(entries) == 0 {
			break
		}
		for _, e := range entries {
			if err := c.store.UpsertCNFINAV(ctx, code, e.date, e.nav, e.ljjz, "eastmoney", time.Now().UTC()); err != nil {
				return total, err
			}
		}
		total += len(entries)
		if len(entries) < 20 {
			break
		}
		time.Sleep(1 * time.Second)
	}
	// R1 断点检测（回填完成后全序列扫描，取最早断点日写入）
	rows, err := c.store.CNFINAVSeries(ctx, code)
	if err == nil {
		series := make([]LJJZPoint, 0, len(rows))
		for _, r := range rows {
			series = append(series, LJJZPoint{Date: r.Date, LJJZ: r.LJJZ})
		}
		if day := DetectCaliberBreak(series); day != nil {
			log.Printf("cnfi: caliber break: code=%s date=%s", code, day.Format("2006-01-02"))
			if err := c.store.SetCNFIInstrumentBreak(ctx, code, *day); err != nil {
				log.Printf("cnfi: set break: %v", err)
			}
		}
	}
	return total, firstErr
}
