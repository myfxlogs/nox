// Package cnfi implements CN-FI (境内人民币固收) data ingestion.
// SSOT: docs/cnfi/CN-FI-P0.5规格包-2026-09-05-ZCode.md v0.2 (D-226/D-229/D-230/D-235).
// Runtime source = chinabond historyQuery (chinamoney rejected 2026-09-05, 403 anti-scrape).
package cnfi

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// Point is one normalized observation destined for cnfi_series.
type Point struct {
	AsOf    string  // 中国市场业务日 YYYY-MM-DD
	Series  string  // cgb / ig_aaa / bank_aaa
	Tenor   string  // 3m / 1y / 3y / 5y / 10y / 30y
	ValueBP float64 // 源百分比 × 100
}

// curveSeries maps chinabond curve names to series keys (spec §1.2; single request carries all three).
var curveSeries = map[string]string{
	"中债国债收益率曲线":           "cgb",
	"中债商业银行普通债收益率曲线(AAA)": "bank_aaa",
	"中债中短期票据收益率曲线(AAA)":   "ig_aaa",
}

// tenorCols maps chinabond column headers to tenor keys.
// 6月/7年 intentionally unmapped (spec §1.1: 不写库); 2y 主源不可达为预留 (复审 P3 注记②).
var tenorCols = map[string]string{
	"3月": "3m", "1年": "1y", "3年": "3y", "5年": "5y", "10年": "10y", "30年": "30y",
}

var (
	trRe   = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	cellRe = regexp.MustCompile(`(?s)<t[dh][^>]*>(.*?)</t[dh]>`)
	tagRe  = regexp.MustCompile(`<[^>]*>`)
	dateRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	numRe  = regexp.MustCompile(`^\d+(\.\d+)?$`)
)

func cleanCell(s string) string {
	s = strings.ReplaceAll(s, "&nbsp;", " ")
	s = tagRe.ReplaceAllString(s, "")
	return strings.TrimSpace(strings.Join(strings.Fields(s), ""))
}

// ParseHistoryHTML parses the chinabond historyQuery HTML table into normalized points.
// Empty cells are skipped (missing semantics, spec §1.1); unknown curves/tenors are
// reported via the returned warning list and must not fail the run (源方可加列).
func ParseHistoryHTML(html string) ([]Point, []string, error) {
	byHeader := map[int]string{} // cell index → tenor
	var points []Point
	var warns []string
	haveHeader := false
	for _, tr := range trRe.FindAllStringSubmatch(html, -1) {
		var cells []string
		for _, c := range cellRe.FindAllStringSubmatch(tr[1], -1) {
			cells = append(cells, cleanCell(c[1]))
		}
		if len(cells) == 0 {
			continue
		}
		if cells[0] == "曲线名称" {
			// 列头判定=结构化（期限列映射非空），不得 grep 原始 HTML——
			// 源方在数字与单位间插注释（如 <td>3<!-- 月 -->月</td>），原始串无连续"3月"。
			// 表单框架行（select 控件）无精确期限列，自然排除。
			byHeader = map[int]string{}
			for i, c := range cells[2:] {
				if t, ok := tenorCols[c]; ok {
					byHeader[i+2] = t
				}
			}
			if len(byHeader) > 0 {
				haveHeader = true
			}
			continue
		}
		if !haveHeader || len(cells) < 2 || !strings.HasPrefix(cells[0], "中债") || !dateRe.MatchString(cells[1]) {
			continue
		}
		series, ok := curveSeries[cells[0]]
		if !ok {
			warns = append(warns, "unknown curve: "+cells[0])
			continue
		}
		for idx, tenor := range byHeader {
			if idx >= len(cells) {
				continue
			}
			v := cells[idx]
			if v == "" || v == "---" {
				continue // missing 语义：空单元格不落库（规格 §1.1）
			}
			if !numRe.MatchString(v) {
				warns = append(warns, fmt.Sprintf("unparsable %s %s %s: %q", series, cells[1], tenor, v))
				continue
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				warns = append(warns, fmt.Sprintf("parse %s %s %s: %v", series, cells[1], tenor, err))
				continue
			}
			points = append(points, Point{AsOf: cells[1], Series: series, Tenor: tenor, ValueBP: f * 100})
		}
	}
	if !haveHeader {
		return nil, warns, fmt.Errorf("cnfi parse: header row (曲线名称+3月) not found — source layout changed or blocked page")
	}
	if len(points) == 0 {
		return nil, warns, fmt.Errorf("cnfi parse: zero data rows")
	}
	return points, warns, nil
}
