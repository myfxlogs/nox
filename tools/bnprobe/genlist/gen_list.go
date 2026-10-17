// gen_list D-608 退回㉛②：binance 端点清单机械生成器——
// 官方 swagger spec（binance.github.io/binance-api-swagger）拉取→
// paths 机械枚举→docs/能力账/binance_endpoints.gen.txt 生成物（禁手写）。
// 真钱端点黑名单分离（mutating 不探如实缺）。
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"
)

const specURL = "https://raw.githubusercontent.com/binance/binance-api-swagger/master/spot_api.yaml"
const listOut = "docs/能力账/binance_endpoints.gen.txt"

// mutating 黑名单（禁跑真钱——标「mutating 未实测」如实缺）
var mutating = []string{
	"/api/v3/order", "/fapi/v1/order", "/sapi/v1/capital/withdraw",
	"/sapi/v1/capital/transfer", "/sapi/v1/asset/transfer",
	"/sapi/v1/simple-earn/flexible/subscribe", "/sapi/v1/simple-earn/flexible/redeem",
	"/sapi/v1/convert",
}

func isMutating(p string) bool {
	for _, m := range mutating {
		if strings.Contains(p, m) {
			return true
		}
	}
	return false
}

type swaggerPaths map[string]map[string]any

func main() {
	c := &http.Client{Timeout: 30 * time.Second}
	resp, err := c.Get(specURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "spec fetch: %v\n", err)
		os.Exit(1)
	}
	defer resp.Body.Close()
	var spec struct {
		Paths swaggerPaths `json:"paths"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&spec); err != nil {
		// YAML 无 encoding——退到行扫描（paths 机械枚举语义不变）
		fmt.Fprintf(os.Stderr, "spec decode（YAML 退行扫描）: %v\n", err)
	}
	var paths []string
	for p := range spec.Paths {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	// YAML 解码败时行扫描兜底（paths 机械枚举不手写）
	if len(paths) == 0 {
		resp2, _ := c.Get(specURL)
		defer resp2.Body.Close()
		var lines []string
		buf := make([]byte, 4096)
		for {
			n, err := resp2.Body.Read(buf)
			if n > 0 {
				lines = append(lines, strings.Split(string(buf[:n]), "\n")...)
			}
			if err != nil {
				break
			}
		}
		for _, l := range lines {
			if strings.HasPrefix(strings.TrimSpace(l), "/api/") ||
				strings.HasPrefix(strings.TrimSpace(l), "/fapi/") ||
				strings.HasPrefix(strings.TrimSpace(l), "/sapi/") {
				if p := strings.SplitN(strings.TrimSpace(l), ":", 2); len(p) > 0 {
					paths = append(paths, strings.TrimSpace(p[0]))
				}
			}
		}
	}
	var out []string
	for _, p := range paths {
		tag := "GET"
		if isMutating(p) {
			tag = "MUTATING（禁跑真钱）"
		}
		out = append(out, tag+"\t"+p)
	}
	if err := os.WriteFile(listOut, []byte(strings.Join(out, "\n")+"\n"), 0644); err != nil {
		fmt.Fprintf(os.Stderr, "write: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("bnprobe gen_list: %d 端点机械枚举→%s（mutating=%d 标缺）\n", len(paths), listOut, len(mutating))
}
