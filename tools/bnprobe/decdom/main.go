// decdom 域级解 pb——按 path 前缀分域打 status 直方+weight 均值+OK 端点清单
// （decpb 只打 FAIL 行——能力账回填需域面全景）。
package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"google.golang.org/protobuf/proto"
	capabilitypb "nox/internal/gen/capability"
)

func domain(p string) string {
	switch {
	case strings.HasPrefix(p, "/fapi/"):
		return "fapi"
	case strings.HasPrefix(p, "/dapi/"):
		return "dapi"
	case strings.HasPrefix(p, "/sapi/"):
		return "sapi"
	case strings.HasPrefix(p, "/wapi/"):
		return "wapi"
	default:
		return "api"
	}
}

func main() {
	b, _ := os.ReadFile(os.Args[1])
	var r capabilitypb.ProbeRun
	if err := proto.Unmarshal(b, &r); err != nil {
		fmt.Println("unmarshal:", err)
		return
	}
	byDom := map[string][]*capabilitypb.EndpointEvidence{}
	for _, e := range r.Evidence {
		byDom[domain(e.Endpoint)] = append(byDom[domain(e.Endpoint)], e)
	}
	var keys []string
	for k := range byDom {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, d := range keys {
		es := byDom[d]
		ok, wsum, wn := 0, 0, 0
		stHist := map[int32]int{}
		for _, e := range es {
			stHist[e.Status]++
			if e.Ok {
				ok++
			}
			var w int
			fmt.Sscanf(e.XMbxUsedWeight, "%d", &w)
			if w > 0 {
				wsum += w
				wn++
			}
		}
		fmt.Printf("=== %s: n=%d ok=%d ===\n", d, len(es), ok)
		for st, n := range stHist {
			fmt.Printf("  st%d=%d", st, n)
		}
		fmt.Println()
		if wn > 0 {
			fmt.Printf("  weight_avg=%d n=%d\n", wsum/wn, wn)
		}
		for _, e := range es {
			if e.Ok {
				kf := e.KeyFieldsNonempty
				if len(kf) > 3 {
					kf = kf[:3]
				}
				fmt.Printf("  OK %s w=%s keys=%v\n", e.Endpoint, e.XMbxUsedWeight, kf)
			}
		}
	}
}
