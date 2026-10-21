// econ.go —— 经济经验库 schema 门禁（D-587 自审三缺口①）：模板校验+索引生成。
// 库面=docs/经济经验库/ 散文式条目（区别于工程库 frontmatter Schema v2）——
// 必填字段：标题（# 行）+「**判据挂接**」EA-xx 指针。索引=条目首行清单自动
// 生成（禁手编——同工程库 索引.md 先例）。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	econIndexName   = "索引.md"
	econEntriesName = "条目"
	econSkipNames   = "README.md|模板.md|待归纳.md|索引.md|裁定账.md"
	econRequiredFm  = "**判据挂接**"
	// econRequiredFixpoint 回改点强制字段（消化链建制票③——无回改点不入库）：
	// eval 词/kill trigger/配对校验闸/仓位面枚举值/semantic:<目标> 五域前缀枚举。
	econRequiredFixpoint = "**回改点**"
)

// econDirConst 可变（resolvePaths repo-root 锚定重写——同三基准先例防 cwd 绑死）。
var econDirConst = "../../docs/经济经验库"

// econFixpointKinds 回改点合法 kind 前缀域（机械类直译成闸/语义类→自动立修订票）。
var econFixpointKinds = []string{"eval:", "kill_trigger:", "combo_gate:", "sizing:", "semantic:"}

// econMatchKeys match: 段合法键域（坑签名，D-587④ 续段③）——与
// econkb.matchEntry 消费键一一对应；段内未知键=签名键名漂移（匹配端读不到
// 即静默失效）→ gen-econ 门禁报错禁入索引。
var econMatchKeys = map[string]bool{
	"payoff_class": true,
	"canonicals":   true,
	"venues":       true,
}

func econDirPath() string {
	if d := os.Getenv("NOX_ECONKB_DIR"); d != "" {
		return d
	}
	return econDirConst
}

// econEntriesDir 条目目（消化链建制票：根目条目迁 条目/ 子目；无子目=根目回落——
// 迁移期双形态兼容）。
func econEntriesDir(dir string) string {
	if st, err := os.Stat(filepath.Join(dir, econEntriesName)); err == nil && st.IsDir() {
		return filepath.Join(dir, econEntriesName)
	}
	return dir
}

// econFixpointOK 回改点值域校验：**回改点** 行存在且值以合法 kind 前缀起。
func econFixpointOK(content string) bool {
	for _, ln := range strings.Split(content, "\n") {
		if i := strings.Index(ln, econRequiredFixpoint); i >= 0 {
			v := strings.TrimSpace(strings.TrimLeft(ln[i+len(econRequiredFixpoint):], "：: "))
			for _, k := range econFixpointKinds {
				if strings.HasPrefix(v, k) {
					return true
				}
			}
		}
	}
	return false
}

// econScan 扫描经济库条目（条目/ 子目优先、根目回落）：*.md 除 skip 名集；
// 每文件校验标题+判据挂接+回改点字段（无回改点不入库票面钉死）。
func econScan(dir string) ([]string, error) {
	dir = econEntriesDir(dir)
	fis, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var titles []string
	for _, fi := range fis {
		n := fi.Name()
		if fi.IsDir() || !strings.HasSuffix(n, ".md") || strings.Contains(econSkipNames, n) {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, n))
		if err != nil {
			return nil, err
		}
		c := string(b)
		if !strings.Contains(c, econRequiredFm) {
			return nil, fmt.Errorf("econ schema: %s 缺 %s 字段", n, econRequiredFm)
		}
		if !econFixpointOK(c) {
			return nil, fmt.Errorf("econ schema: %s 缺 %s 或 kind 前缀非法（域=eval:|kill_trigger:|combo_gate:|sizing:|semantic:）", n, econRequiredFixpoint)
		}
		if err := econValidateMatchSeg(n, c); err != nil {
			return nil, err
		}
		line, _, _ := strings.Cut(c, "\n")
		titles = append(titles, fmt.Sprintf("- %s —— `%s`", strings.TrimPrefix(strings.TrimSpace(line), "# "), n))
	}
	sort.Strings(titles)
	return titles, nil
}

// econValidateMatchSeg 正文 match: 段键域校验（段=顶格 `match:` 行至首个非缩进行）。
// 段内键 ∉ econMatchKeys → 报错（坑签名键名漂移防护——匹配端 econkb 只认三键，
// 写错键名=该签名永不命中且无告警，门禁单点拦）。
func econValidateMatchSeg(name, content string) error {
	inSeg := false
	for _, ln := range strings.Split(content, "\n") {
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
		i := strings.Index(tt, ":")
		if i <= 0 {
			continue // 无键值形行（注释等）不属键域
		}
		key := strings.TrimSpace(tt[:i])
		if !econMatchKeys[key] {
			return fmt.Errorf("econ schema: %s match 段未知键 %q（合法域=payoff_class/canonicals/venues——与 econkb.matchEntry 消费键对齐）", name, key)
		}
	}
	return nil
}

// econGen 生成/校验 索引.md（check 模式只比对不落盘）。
func econGen(check bool) error {
	dir := econDirPath()
	titles, err := econScan(dir)
	if err != nil {
		return err
	}
	var w strings.Builder
	w.WriteString("# 经济经验库·索引（noxkb gen-econ 自动生成禁手编）\n\n")
	for _, t := range titles {
		w.WriteString(t + "\n")
	}
	idx := filepath.Join(dir, econIndexName)
	if check {
		cur, err := os.ReadFile(idx)
		if err != nil || string(cur) != w.String() {
			return fmt.Errorf("经济经验库索引过期——跑 noxkb gen-econ 重生成")
		}
		return nil
	}
	return os.WriteFile(idx, []byte(w.String()), 0o644)
}
