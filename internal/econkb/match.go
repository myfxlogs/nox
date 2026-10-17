// Package econkb —— D-587④ 坑签名匹配纯函数（只读呈现不拦闸）。
// 条目散文式：match: 段（**判据挂接** 行后）缩进键行——
//
//	match:
//	  payoff_class: naked_short_vol      # 精确相等
//	  canonicals: [A, B]                 # 任一命中即中
//	  venues: [binance, gate]            # 任一小写 venue 名命中即中
//
// 全部非空键过才命中（保守=宁漏不误中）；无 match 段=不参与匹配。
package econkb

import (
	"os"
	"path/filepath"
	"strings"
)

// Input 匹配输入（各键可空——空键不约束）。
type Input struct {
	PayoffClass string
	Canonical   string
	Canonicals  []string // 双腿 canonical 集（任一命中即中——armed 组合闸用）
	VenueA      string
	VenueB      string
}

// Hit 命中条目。
type Hit struct {
	ID     string // 文件名去 .md
	EALink string
}

// MatchHits 读条目目录→逐条 match 段解析→输入对拍→命中集。
// 库缺/读败=空集透出非阻塞（禁线：不拦闸）。
func MatchHits(dir string, in Input) []Hit {
	fis, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var hits []Hit
	for _, fi := range fis {
		if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".md") {
			continue
		}
		if fi.Name() == "README.md" || fi.Name() == "待归纳.md" || fi.Name() == "模板.md" || fi.Name() == "索引.md" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, fi.Name()))
		if err != nil {
			continue
		}
		if matchEntry(string(data), in) {
			hits = append(hits, Hit{ID: strings.TrimSuffix(fi.Name(), ".md"), EALink: eaLinkOf(string(data))})
		}
	}
	return hits
}

// parseMatch 抽 match: 段缩进键行（至空行/非缩进行止）。
func parseMatch(content string) map[string]string {
	out := map[string]string{}
	lines := strings.Split(content, "\n")
	inSeg := false
	for _, ln := range lines {
		t := strings.TrimRight(ln, " \t")
		if !inSeg {
			if strings.HasPrefix(t, "match:") {
				inSeg = true
			}
			continue
		}
		if !strings.HasPrefix(ln, " ") && !strings.HasPrefix(ln, "\t") {
			break // 段终
		}
		tt := strings.TrimSpace(t)
		if tt == "" {
			break
		}
		if i := strings.Index(tt, ":"); i > 0 {
			out[strings.TrimSpace(tt[:i])] = strings.TrimSpace(tt[i+1:])
		}
	}
	return out
}

func matchEntry(content string, in Input) bool {
	m := parseMatch(content)
	if len(m) == 0 {
		return false
	}
	if v := m["payoff_class"]; v != "" && v != in.PayoffClass {
		return false
	}
	if v := m["canonicals"]; v != "" {
		keys := append([]string{in.Canonical}, in.Canonicals...)
		ok := false
		for _, k := range keys {
			if k != "" && listHit(v, k) {
				ok = true
				break
			}
		}
		if !ok {
			return false
		}
	}
	if v := m["venues"]; v != "" {
		ok := (in.VenueA != "" && listHit(v, in.VenueA)) || (in.VenueB != "" && listHit(v, in.VenueB))
		if !ok {
			return false
		}
	}
	return true
}

// listHit "[a, b]" 或 "a" 形式集命中。
func listHit(listExpr, key string) bool {
	for _, item := range strings.Fields(strings.NewReplacer("[", " ", "]", " ", ",", " ").Replace(listExpr)) {
		if strings.EqualFold(item, key) {
			return true
		}
	}
	return false
}

// eaLinkOf 抽 "**判据挂接**" 行内 EA-xx 首枚。
func eaLinkOf(content string) string {
	for _, ln := range strings.Split(content, "\n") {
		if strings.Contains(ln, "判据挂接") {
			if i := strings.Index(ln, "EA-"); i >= 0 {
				j := i + 3
				for j < len(ln) && (ln[j] >= '0' && ln[j] <= '9' || ln[j] == '-') {
					j++
				}
				return ln[i:j]
			}
		}
	}
	return ""
}

// Dir econ 库目探测（econKBDir 同序——env 覆写优先，/kn/docs 容器面，相对回落）。
func Dir() string {
	if v := os.Getenv("NOX_ECONKB_DIR"); v != "" {
		return v
	}
	if _, err := os.Stat("/kn/docs/经济经验库"); err == nil {
		return "/kn/docs/经济经验库"
	}
	return "docs/经济经验库"
}

// PitLabels 命中标签集（毕业卡/armed 面板共用——「⚠命中坑 <id>（EA-xx）」）。
func PitLabels(dir string, in Input) []string {
	hits := MatchHits(dir, in)
	out := make([]string, 0, len(hits))
	for _, h := range hits {
		label := "⚠命中坑 " + h.ID
		if h.EALink != "" {
			label += "（" + h.EALink + "）"
		}
		out = append(out, label)
	}
	return out
}

// PitBlocked D-587④ armed 闸判据：命中坑签名且 pit_exempt_d 未落豁免引用 → true。
// 豁免=D# 显式引用留痕（误判可开豁，禁默认）——签名保守语义不动（宁漏不误中）。
func PitBlocked(dir string, in Input, exemptD string) (blocked bool, hits []Hit) {
	hits = MatchHits(dir, in)
	if len(hits) == 0 || exemptD != "" {
		return false, hits
	}
	return true, hits
}
