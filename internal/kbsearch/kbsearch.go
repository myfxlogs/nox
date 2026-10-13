// kbsearch.go —— 经验库条目关键词对拍共享件（拒单消费闸：坑签名→条目 id+summary 回流
// 告警面；analyst kb_search 工具同源）。纯进程内对拍零 LLM 零子进程；读目/解析败
// fail-open 由调用方兜（返回空集，绝不阻塞主径）。
// 高置信门槛复用 induct covered 同规则（条目 kb-covered-match-needs-multihit）：
// 忽略 <4 字符短词；≥2 关键词命中，或单命中关键词 ≥12 字符（指纹级）。
package kbsearch

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"gopkg.in/yaml.v3"
)

// Entry 经验库条目检索视图（frontmatter 子集）。
type Entry struct {
	ID       string
	Summary  string
	Keywords []string
	Status   string
}

// Hit 命中条目+判据留痕（hits=关键词命中数；kw=最长命中词）。
type Hit struct {
	Entry
	Hits   int
	LongKW string
}

// Load 读 dir 下 *.md frontmatter（retired 条目剔除——退休知识不贴告警脸）。
// 目录缺席/读败 → error（调用方 fail-open 处理）；单文件解析败跳过该件。
func Load(dir string) ([]Entry, error) {
	fis, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, fi := range fis {
		if fi.IsDir() || !strings.HasSuffix(fi.Name(), ".md") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, fi.Name()))
		if err != nil {
			continue
		}
		fm := frontmatter(raw)
		if fm == "" {
			continue
		}
		var e struct {
			ID       string   `yaml:"id"`
			Summary  string   `yaml:"summary"`
			Keywords []string `yaml:"keywords"`
			Status   string   `yaml:"status"`
		}
		if err := yaml.Unmarshal([]byte(fm), &e); err != nil || e.ID == "" || e.Status == "retired" {
			continue
		}
		out = append(out, Entry{ID: e.ID, Summary: e.Summary, Keywords: e.Keywords, Status: e.Status})
	}
	return out, nil
}

// frontmatter 抽 `---\n...\n---` 段（noxkb 同格式）。
func frontmatter(raw []byte) string {
	s := string(raw)
	if !strings.HasPrefix(s, "---") {
		return ""
	}
	rest := s[3:]
	i := strings.Index(rest, "\n---")
	if i < 0 {
		return ""
	}
	return rest[:i]
}

// Match 高置信对拍：text 小写化后逐条目计数关键词子串命中——
// hits≥2 或单命中词 ≥12 字符（指纹级）才算覆盖；多条达标按 hits 降序、
// 再按最长命中词降序取首。零达标 → nil（宁缺不滥）。
func Match(entries []Entry, text string) *Hit {
	hits := Search(entries, text)
	if len(hits) == 0 {
		return nil
	}
	return &hits[0]
}

// Search 全量达标命中排序（hits desc → 最长命中词 desc → id 序），analyst 工具用。
func Search(entries []Entry, text string) []Hit {
	low := strings.ToLower(text)
	var out []Hit
	for _, e := range entries {
		var h Hit
		h.Entry = e
		for _, kw := range e.Keywords {
			k := strings.ToLower(strings.TrimSpace(kw))
			if utf8.RuneCountInString(k) < 4 || !strings.Contains(low, k) {
				continue
			}
			h.Hits++
			if len(k) > len(h.LongKW) {
				h.LongKW = k
			}
		}
		if h.Hits >= 2 || utf8.RuneCountInString(h.LongKW) >= 12 {
			out = append(out, h)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Hits != out[j].Hits {
			return out[i].Hits > out[j].Hits
		}
		if len(out[i].LongKW) != len(out[j].LongKW) {
			return len(out[i].LongKW) > len(out[j].LongKW)
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// Suffix 告警附行便捷件：dir 读败/零命中 → ""（fail-open 零阻塞）；
// 命中 → ` kb:<id> <summary>`（拼 alert 正文尾）。
func Suffix(dir, text string) string {
	entries, err := Load(dir)
	if err != nil {
		return ""
	}
	h := Match(entries, text)
	if h == nil {
		return ""
	}
	sum := h.Summary
	if utf8.RuneCountInString(sum) > 80 {
		sum = string([]rune(sum)[:80])
	}
	return " kb:" + h.ID + " " + sum
}
