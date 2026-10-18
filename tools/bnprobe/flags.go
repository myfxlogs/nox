// flags.go —— 棘轮拆出件（main.go 超 300 行分流）：CLI flag 解析+skip 跳表载。
package main

import (
	"fmt"
	"os"
	"strings"
)

// parseFlags——阈值单源在 CLI（-weightcap/-weightpct——禁写死多处）；
// -skip=残段补跑已记实端点跳表文件（一行一端点，免重复烧共享权重）。
func parseFlags() (offset, limit, weightCap, weightPct int, skip map[string]bool) {
	weightCap = 1200 // 共享 IP 分钟预算上限（保守缺省——生产 REST 面大头后探针配额）
	weightPct = 80   // 自限阈=cap×80%（缺省——超阈即截非 418 撞墙）
	skipFile := ""   // 残段补跑——已记实端点跳表
	for i, a := range os.Args {
		if i+1 >= len(os.Args) {
			break
		}
		switch a {
		case "-offset":
			fmt.Sscanf(os.Args[i+1], "%d", &offset)
		case "-limit":
			fmt.Sscanf(os.Args[i+1], "%d", &limit)
		case "-weightcap":
			fmt.Sscanf(os.Args[i+1], "%d", &weightCap)
		case "-weightpct":
			fmt.Sscanf(os.Args[i+1], "%d", &weightPct)
		case "-skip":
			skipFile = os.Args[i+1]
		}
	}
	skip = map[string]bool{}
	if skipFile == "" {
		return
	}
	sb, err := os.ReadFile(skipFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "skip file: %v\n", err)
		os.Exit(1)
	}
	for _, l := range strings.Split(string(sb), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			skip[l] = true
		}
	}
	return
}
