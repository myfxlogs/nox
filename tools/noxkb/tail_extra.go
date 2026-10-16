// tail_extra.go —— 棘轮拆出件（main.go 超 300 行分流）。
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: noxkb gen | gen --check | induct")
		os.Exit(2)
	}
	applyKBDirEnv() // NOX_KB_DIR 工具引导覆写（同 digest 先例）
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
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", os.Args[1])
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
