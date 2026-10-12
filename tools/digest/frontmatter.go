// frontmatter.go 条目 frontmatter 解析（S3.3 staleness 用，独立实现，不 import arbkb）。
// 仅解析 staleness 所需字段：id / last_verified / evidence_refs。
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// frontmatterEntry 条目 frontmatter（仅 staleness 所需字段）。
type frontmatterEntry struct {
	ID           string                   `yaml:"id"`
	LastVerified string                   `yaml:"last_verified"`
	EvidenceRefs []map[string]interface{} `yaml:"evidence_refs"`
}

// parseFrontmatter 解析单个条目文件的 frontmatter（--- YAML ---）。
func parseFrontmatter(data []byte) (*frontmatterEntry, error) {
	content := string(data)
	var fmStart int
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
	fmBytes := []byte(content[fmStart : fmStart+end])
	var e frontmatterEntry
	if err := yaml.Unmarshal(fmBytes, &e); err != nil {
		return nil, fmt.Errorf("yaml unmarshal: %w", err)
	}
	if e.ID == "" {
		return nil, fmt.Errorf("id is empty")
	}
	return &e, nil
}

// loadFrontmatterEntries 扫描条目目录，解析所有 .md 文件的 frontmatter（staleness 用）。
func loadFrontmatterEntries(dir string) ([]frontmatterEntry, error) {
	files, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read dir %s: %w", dir, err)
	}
	var entries []frontmatterEntry
	for _, f := range files {
		if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, f.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f.Name(), err)
		}
		e, err := parseFrontmatter(data)
		if err != nil {
			continue // 跳过解析失败的条目（不阻断 digest）
		}
		entries = append(entries, *e)
	}
	return entries, nil
}
