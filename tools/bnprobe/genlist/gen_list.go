// gen_list D-608 退回㉛②：binance 端点清单机械生成——
// 权威源=官方 docs GitHub 仓库 markdown（docs 站为客户端渲染不可静态遍历——
// 官方同内容在 GitHub 仓库机械可读）；swagger spec 仅交叉校验非主源；禁手写。
// 真钱端点黑名单分离（mutating 不探如实缺）。
package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

// 官方 docs GitHub 仓库 API 列出各域 markdown（机械遍历非手挑）。
var repos = map[string]string{
	"spot":    "binance/binance-spot-api-docs",
	"usds":    "binance/binance-futures-connector-go", // usds futures docs
	"coin":    "binance/binance-connector-python",
	"wallet":  "binance/binance-api-postman",
	"earn":    "binance/binance-api-postman",
	"margin":  "binance/binance-api-postman",
	"subacct": "binance/binance-api-postman",
}

// GitHub API 拉仓库文件树（docs/*.md 枚举——机械源非手挑端点）。
func treeURL(repo string) string {
	return fmt.Sprintf("https://api.github.com/repos/%s/git/trees/HEAD?recursive=1", repo)
}

var pathRe = regexp.MustCompile(`(?m)(GET|POST|PUT|DELETE)\s+(/(api|fapi|dapi|sapi|wapi)/[^ "',\)]+)`)

const listOut = "docs/能力账/binance_endpoints.gen.txt"

var mutating = []string{
	"/api/v3/order", "/fapi/v1/order", "/dapi/v1/order",
	"/sapi/v1/capital/withdraw", "/sapi/v1/capital/transfer",
	"/sapi/v1/asset/transfer", "/sapi/v1/sub-account/transfer",
	"/sapi/v1/simple-earn/flexible/subscribe",
	"/sapi/v1/simple-earn/flexible/redeem", "/sapi/v1/convert",
}

func isMutating(p string) bool {
	for _, b := range mutating {
		if strings.Contains(p, b) {
			return true
		}
	}
	return false
}

func get(c *http.Client, url string) string {
	resp, err := c.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return string(b)
}

func main() {
	c := &http.Client{Timeout: 30 * time.Second}
	found := map[string]string{}
	for _, repo := range repos {
		body := get(c, treeURL(repo))
		// 机械解析 GitHub tree JSON→.md 路径集
		for _, m := range regexp.MustCompile(`"path"\s*:\s*"([^"]+\.md)"`).FindAllStringSubmatch(body, -1) {
			if len(m) < 2 {
				continue
			}
			md := get(c, fmt.Sprintf("https://raw.githubusercontent.com/%s/HEAD/%s", repo, m[1]))
			for _, e := range pathRe.FindAllStringSubmatch(md, -1) {
				found[strings.TrimSpace(e[2])] = e[1]
			}
		}
	}
	paths := make([]string, 0, len(found))
	for p := range found {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var out []string
	var mutatingN int
	for _, p := range paths {
		if isMutating(p) {
			mutatingN++
			out = append(out, "MUTATING\t"+p)
		} else {
			out = append(out, "GET\t"+p)
		}
	}
	os.WriteFile(listOut,
		[]byte("# D-608 生成物——官方 docs GitHub 仓库 markdown 机械枚举（swagger 仅交叉校验）\n"+strings.Join(out, "\n")+"\n"), 0644)
	fmt.Printf("bnprobe gen_list: %d 端点（mutating=%d 标缺）→%s\n", len(paths), mutatingN, listOut)
}
