// deceps —— pb 证据件端点名直解器（残段补跑 skipfile 生成用）：
// 逐 pb 解 ProbeRun.Evidence.Endpoint 去重输出（stdout 一行一端点）。
package main

import (
	"fmt"
	"os"

	capabilitypb "nox/internal/gen/capability"

	"google.golang.org/protobuf/proto"
)

func main() {
	seen := map[string]bool{}
	for _, f := range os.Args[1:] {
		b, err := os.ReadFile(f)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read:", err)
			continue
		}
		var r capabilitypb.ProbeRun
		if err := proto.Unmarshal(b, &r); err != nil {
			fmt.Fprintln(os.Stderr, "unmarshal:", err)
			continue
		}
		for _, e := range r.Evidence {
			if !seen[e.Endpoint] {
				seen[e.Endpoint] = true
				fmt.Println(e.Endpoint)
			}
		}
	}
}
