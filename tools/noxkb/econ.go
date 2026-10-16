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
	econDirConst   = "../../docs/经济经验库"
	econIndexName  = "索引.md"
	econSkipNames  = "README.md|模板.md|待归纳.md|索引.md"
	econRequiredFm = "**判据挂接**"
)

func econDirPath() string {
	if d := os.Getenv("NOX_ECONKB_DIR"); d != "" {
		return d
	}
	return econDirConst
}

// econScan 扫描经济库条目：*.md 除 skip 名集；每文件校验标题+判据挂接字段。
func econScan(dir string) ([]string, error) {
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
		line, _, _ := strings.Cut(c, "\n")
		titles = append(titles, fmt.Sprintf("- %s —— `%s`", strings.TrimPrefix(strings.TrimSpace(line), "# "), n))
	}
	sort.Strings(titles)
	return titles, nil
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
