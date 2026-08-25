// Package main implements arbkb — Arb 本地经验库索引生成器。
// P0 只实现 gen / gen --check。不连接 PG，不执行条目命令，不被 core import。
package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// 路径基准（定死，禁路径探测逻辑）
var (
	entriesDir = "../../docs/经验库/条目"
	indexFile  = "../../docs/经验库/索引.md"
)

const (
	maxLines = 200
	// maxBytes 索引字节硬顶（grep 检索面预算）。2026-08-23 8192→10240：条目增至 25 条后
	// 结构性触顶（25×~330B），削条目内容迁就顶=削足适履；25% 上浮仍为廉价 grep 面，30 条内免再动。
	// 2026-08-24 10240→12800：27 条即 10427B（≈386B/条，中文 summary 3B/字为主因，原估 330B 偏乐观）。
	// 2026-08-25 12800→15360：同日 D-104~D-108 高产 +5 条达 36 条 13124B（≈365B/条），
	// 15360 覆盖 ~42 条；结构性上浮同 8-23 先例。
	maxBytes    = 15360
	maxKeywords = 5
)

// Entry 对应条目 frontmatter（设计 §4.2 Schema v2）。
type Entry struct {
	ID            string                   `yaml:"id"`
	Kind          string                   `yaml:"kind"`
	Risk          string                   `yaml:"risk"`
	Status        string                   `yaml:"status"`
	Keywords      []string                 `yaml:"keywords"`
	Scope         []string                 `yaml:"scope"`
	Summary       string                   `yaml:"summary"`
	SchemaVersion int                      `yaml:"schema_version"`
	FirstSeen     string                   `yaml:"first_seen"`
	LastVerified  string                   `yaml:"last_verified"`
	Skills        []string                 `yaml:"skills"`
	EvidenceRefs  []map[string]interface{} `yaml:"evidence_refs"`
}

// header 是索引文件的固定头部（标题 + 说明 + 表头 + 分隔行）。
const header = `# Arb 项目经验索引

> 任务开始时读取一次；命中后读 docs/经验库/条目/<id>.md 全文（P1 起可用 arbkb read <id>）。

| id | kind | keywords | summary | status | risk |
|----|------|----------|---------|--------|------|
`

// parse 解析单个条目文件的 frontmatter（--- YAML ---）。
func parse(data []byte) (*Entry, error) {
	content := string(data)
	start := strings.Index(content, "\n---\n")
	if start < 0 {
		return nil, fmt.Errorf("frontmatter not found")
	}
	// 跳过第一个 ---（文件开头或其后）
	fmStart := 0
	if strings.HasPrefix(content, "---\n") {
		fmStart = 4
	} else {
		// 找第一个 --- 行
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
	fmBytes := []byte(content[fmStart : fmStart+end])

	var e Entry
	if err := yaml.Unmarshal(fmBytes, &e); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if e.ID == "" {
		return nil, fmt.Errorf("id is empty")
	}
	return &e, nil
}

// sanitize 转义单元格内的特殊字符：| → \|，剔除换行/制表。
func sanitize(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\t", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	return strings.TrimSpace(s)
}

// keywordsCell 取前 maxKeywords 个关键词，空格连接。
func keywordsCell(kw []string) string {
	if len(kw) > maxKeywords {
		kw = kw[:maxKeywords]
	}
	return sanitize(strings.Join(kw, " "))
}

// renderRow 渲染单行索引。
func renderRow(e *Entry) string {
	return fmt.Sprintf("| %s | %s | %s | %s | %s | %s |",
		sanitize(e.ID), sanitize(e.Kind), keywordsCell(e.Keywords),
		sanitize(e.Summary), sanitize(e.Status), sanitize(e.Risk))
}

// render 从已解析条目列表生成索引字节输出。
func render(entries []*Entry) []byte {
	sort.Slice(entries, func(i, j int) bool {
		return entries[i].ID < entries[j].ID
	})
	var buf bytes.Buffer
	buf.WriteString(header)
	for _, e := range entries {
		buf.WriteString(renderRow(e))
		buf.WriteByte('\n')
	}
	return buf.Bytes()
}

// loadEntries 扫描条目目录，解析所有 .md 文件的 frontmatter。
func loadEntries(dir string) ([]*Entry, error) {
	matches, err := filepath.Glob(filepath.Join(dir, "*.md"))
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}
	var entries []*Entry
	for _, m := range matches {
		data, err := os.ReadFile(m)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", m, err)
		}
		e, err := parse(data)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", filepath.Base(m), err)
		}
		entries = append(entries, e)
	}
	return entries, nil
}

// checkBudget 校验输出是否超预算。
func checkBudget(output []byte) error {
	lines := bytes.Count(output, []byte{'\n'})
	if lines > maxLines {
		return fmt.Errorf("index %d lines exceeds max %d", lines, maxLines)
	}
	if len(output) > maxBytes {
		return fmt.Errorf("index %d bytes exceeds max %d", len(output), maxBytes)
	}
	return nil
}

// gen 生成索引并写入磁盘（幂等：相同则不写）。
func gen() error {
	entries, err := loadEntries(entriesDir)
	if err != nil {
		return err
	}
	output := render(entries)
	if err := checkBudget(output); err != nil {
		return err
	}
	existing, err := os.ReadFile(indexFile)
	if err == nil && bytes.Equal(existing, output) {
		return nil // 幂等，不写
	}
	return os.WriteFile(indexFile, output, 0644)
}

// genCheck 只生成到内存与磁盘 diff，不一致 exit 1。
func genCheck() error {
	entries, err := loadEntries(entriesDir)
	if err != nil {
		return err
	}
	output := render(entries)
	if err := checkBudget(output); err != nil {
		return err
	}
	existing, err := os.ReadFile(indexFile)
	if err != nil {
		return fmt.Errorf("index file missing: %w", err)
	}
	if !bytes.Equal(existing, output) {
		return fmt.Errorf("index out of date")
	}
	return nil
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: arbkb gen | gen --check")
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "gen":
		if len(os.Args) > 2 && os.Args[2] == "--check" {
			err = genCheck()
		} else {
			err = gen()
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
