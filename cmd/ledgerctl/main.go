// cmd/ledgerctl —— 账本工具统一入口（cmd/resettle+cmd/settleonce 合并，D-218 ①/D-212 拆搬）。
//
//	ledgerctl resettle -pos=498 -dry-run     # 重结算（语义见 resettle.go 头注）
//	ledgerctl settleonce -pos=479           # 单仓补结算（语义见 settleonce.go 头注）
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: ledgerctl <resettle|settleonce> [flags]")
		os.Exit(2)
	}
	switch os.Args[1] {
	case "resettle":
		runResettle()
	case "settleonce":
		runSettleOnce()
	default:
		fmt.Fprintf(os.Stderr, "unknown subcommand %q\n", os.Args[1])
		os.Exit(2)
	}
}
