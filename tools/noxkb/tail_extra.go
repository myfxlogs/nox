// tail_extra.go —— 棘轮拆出件（main.go 超 300 行分流）。
package main

import (
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noxkb gen | gen --check | induct")
		os.Exit(2)
	}
	applyKBDirEnv() // NOX_KB_DIR 工具引导覆写（同 digest 先例）
	if err := resolvePaths(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	var err error
	switch os.Args[1] {
	case "gen-econ":
		err = econGen(len(os.Args) > 2 && os.Args[2] == "--check")
	case "gen":
		if len(os.Args) > 2 && os.Args[2] == "--check" {
			err = genCheck()
		} else {
			err = gen()
		}
	case "induct":
		err = inductCmd()
	case "econ-verdict":
		err = econVerdictCmd()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// resolvePaths cwd 绑死坑修：默认路径基准从「cwd 相对 ../../」改「repo root 锚定」——
// 自 cwd 上溯找 docs/经验库/条目 目录（repo root 标记），找到即把三基准重写成绝对径；
// NOX_KB_DIR 已覆写则跳过（env 优先）；找不到=显式报错（禁静默写丢——错 cwd 跑 gen
// 曾产「read 空/写错位」静默败形态）。
func resolvePaths() error {
	if os.Getenv("NOX_KB_DIR") != "" {
		return nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getwd: %w", err)
	}
	for {
		base := filepath.Join(dir, "docs", "经验库")
		if st, err := os.Stat(filepath.Join(base, "条目")); err == nil && st.IsDir() {
			entriesDir = filepath.Join(base, "条目")
			indexFile = filepath.Join(base, "索引.md")
			skillsIndexFile = filepath.Join(base, "技能清单.md")
			econDirConst = filepath.Join(dir, "docs", "经济经验库") // 同锚防 cwd 绑死
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return fmt.Errorf("noxkb: repo root not found (no docs/经验库/条目 ancestor from cwd)")
		}
		dir = parent
	}
}
