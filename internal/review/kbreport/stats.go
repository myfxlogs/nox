// Package kbreport AI 经验库元分析定期报告（D-442）。
// 分层：数字全部由 Go 确定性计算（本文件），AI 只写综合（report.go）——防 LLM 编数。
package kbreport

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// entryFM 条目 frontmatter 最小字段集（Schema v2 子集；自解析不重构 arbkb 共享，D-442 边界）。
type entryFM struct {
	ID           string   `yaml:"id"`
	Kind         string   `yaml:"kind"`
	Risk         string   `yaml:"risk"`
	Status       string   `yaml:"status"`
	Keywords     []string `yaml:"keywords"`
	Scope        []string `yaml:"scope"`
	FirstSeen    string   `yaml:"first_seen"`
	LastVerified string   `yaml:"last_verified"`
}

// Pair 频率对（top_scope/top_keywords 用）。
type Pair struct {
	Name  string
	Count int
}

// Stats 统计快照（Generate §统计快照 fenced 块的唯一数字源）。
type Stats struct {
	Total        int
	ByKind       map[string]int
	ByRisk       map[string]int
	ByStatus     map[string]int
	TopScope     []Pair // Top10
	TopKeywords  []Pair // Top10
	StaleLT30    int    // last_verified <30d
	Stale30To90  int    // 30-90d
	StaleGT90    int    // >90d
	StaleMissing int    // last_verified 缺失/不可解析
	QualityGaps  []string
	RecentAdded  int // first_seen 近 30d 计数
}

const qualityGapCap = 20

// Scan 扫描 root 下 flat *.md（归档/ 子目录天然排除，D-440 同机制）统计 frontmatter。
// root=条目目录本身（如 /kn/docs/经验库/条目）。now 由调用方注入保证分桶确定性。
func Scan(root string, now time.Time) (Stats, error) {
	matches, err := filepath.Glob(filepath.Join(root, "*.md"))
	if err != nil {
		return Stats{}, fmt.Errorf("glob: %w", err)
	}
	s := Stats{
		ByKind:   map[string]int{},
		ByRisk:   map[string]int{},
		ByStatus: map[string]int{},
	}
	scopeFreq := map[string]int{}
	kwFreq := map[string]int{}
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return Stats{}, fmt.Errorf("read %s: %w", m, err)
		}
		e, err := parseFM(data)
		if err != nil {
			return Stats{}, fmt.Errorf("parse %s: %w", filepath.Base(m), err)
		}
		s.Total++
		s.ByKind[e.Kind]++
		s.ByRisk[e.Risk]++
		s.ByStatus[e.Status]++
		for _, sc := range e.Scope {
			scopeFreq[sc]++
		}
		for _, kw := range e.Keywords {
			kwFreq[kw]++
		}
		// 陈旧度分桶（last_verified 相对 now 的天龄）
		lv := strings.TrimSpace(e.LastVerified)
		if d, perr := time.Parse("2006-01-02", lv); perr == nil {
			switch age := now.Sub(d); {
			case age < 30*24*time.Hour:
				s.StaleLT30++
			case age < 90*24*time.Hour:
				s.Stale30To90++
			default:
				s.StaleGT90++
			}
		} else {
			s.StaleMissing++
		}
		// 质量缺项：正文无 falsifies_when 节
		if !strings.Contains(string(data), "falsifies_when") && len(s.QualityGaps) < qualityGapCap {
			s.QualityGaps = append(s.QualityGaps, e.ID)
		}
		// 近 30d 新增
		if d, perr := time.Parse("2006-01-02", strings.TrimSpace(e.FirstSeen)); perr == nil && now.Sub(d) < 30*24*time.Hour {
			s.RecentAdded++
		}
	}
	s.TopScope = topN(scopeFreq, 10)
	s.TopKeywords = topN(kwFreq, 10)
	return s, nil
}

// parseFM 解 frontmatter（--- YAML ---；同 arbkb parse 的跳过首行策略）。
func parseFM(data []byte) (*entryFM, error) {
	content := string(data)
	fmStart := 0
	if strings.HasPrefix(content, "---\n") {
		fmStart = 4
	} else {
		idx := strings.Index(content, "---\n")
		if idx < 0 {
			return nil, fmt.Errorf("frontmatter opening not found")
		}
		fmStart = idx + 4
	}
	end := strings.Index(content[fmStart:], "\n---\n")
	if end < 0 {
		return nil, fmt.Errorf("frontmatter closing not found")
	}
	var e entryFM
	if err := yaml.Unmarshal([]byte(content[fmStart:fmStart+end]), &e); err != nil {
		return nil, fmt.Errorf("yaml: %w", err)
	}
	if e.ID == "" {
		return nil, fmt.Errorf("id is empty")
	}
	return &e, nil
}

// topN 频率表取前 n（计数降序，同计数按名字序保证确定性）。
func topN(freq map[string]int, n int) []Pair {
	out := make([]Pair, 0, len(freq))
	for k, v := range freq {
		out = append(out, Pair{k, v})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// RenderMarkdown 产 §统计快照 fenced 块内容（fenced 由 report 模板包裹）。
func RenderMarkdown(s Stats) string {
	var b strings.Builder
	fmt.Fprintf(&b, "total: %d\n", s.Total)
	writeMap := func(name string, m map[string]int) {
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		parts := make([]string, 0, len(keys))
		for _, k := range keys {
			parts = append(parts, fmt.Sprintf("%s=%d", k, m[k]))
		}
		fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(parts, " "))
	}
	writeMap("by_kind", s.ByKind)
	writeMap("by_risk", s.ByRisk)
	writeMap("by_status", s.ByStatus)
	writePairs := func(name string, ps []Pair) {
		parts := make([]string, 0, len(ps))
		for _, p := range ps {
			parts = append(parts, fmt.Sprintf("%s=%d", p.Name, p.Count))
		}
		fmt.Fprintf(&b, "%s: %s\n", name, strings.Join(parts, " "))
	}
	writePairs("top_scope", s.TopScope)
	writePairs("top_keywords", s.TopKeywords)
	fmt.Fprintf(&b, "stale_buckets: <30d=%d 30-90d=%d >90d=%d missing=%d\n",
		s.StaleLT30, s.Stale30To90, s.StaleGT90, s.StaleMissing)
	fmt.Fprintf(&b, "recent_added_30d: %d\n", s.RecentAdded)
	if len(s.QualityGaps) > 0 {
		fmt.Fprintf(&b, "quality_gaps_no_falsifies_when (%d, cap %d): %s\n",
			len(s.QualityGaps), qualityGapCap, strings.Join(s.QualityGaps, " "))
	} else {
		b.WriteString("quality_gaps_no_falsifies_when: none\n")
	}
	return b.String()
}
