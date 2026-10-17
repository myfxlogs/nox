package main

import (
	"fmt"
	"os"

	capabilitypb "nox/internal/gen/capability"

	"google.golang.org/protobuf/proto"
)

func main() {
	b, _ := os.ReadFile(os.Args[1])
	var r capabilitypb.ProbeRun
	if err := proto.Unmarshal(b, &r); err != nil {
		fmt.Println("unmarshal:", err)
		return
	}
	fmt.Printf("gen=%s gw=%s evidence=%d raw=%d\n", r.GeneratedAt, r.Gateway, len(r.Evidence), len(r.RawBodies))
	statusMap := map[int32]int{}
	var sampleFail []string
	for _, e := range r.Evidence {
		statusMap[e.Status]++
		if !e.Ok && len(sampleFail) < 10 {
			sampleFail = append(sampleFail, fmt.Sprintf("%s st=%d err=%s", e.Endpoint, e.Status, e.Err))
		}
	}
	for k, v := range statusMap {
		fmt.Printf("status %d: %d\n", k, v)
	}
	for _, s := range sampleFail {
		fmt.Println("FAIL:", s)
	}
}
