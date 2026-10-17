// gen_list D-608：binance 端点清单机械生成——官方 docs GitHub 仓库 markdown
// 遍历；swagger 仅交叉校验；禁手写。退回㉜①：正则字符类排反引号+空格右括。
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

var repos = []string{
	"binance/binance-spot-api-docs",
	"binance/binance-futures-connector-go",
	"binance/binance-connector-python",
	"binance/binance-api-postman",
}

var pathRe = regexp.MustCompile(`(?m)(GET|POST|PUT|DELETE)\s+(/(api|fapi|dapi|sapi|wapi)/[a-zA-Z0-9_\-/.{}]+)`)

const listOut = "docs/能力账/binance_endpoints.gen.txt"

// method+path 二元键黑名单——POST 同径株连修（退回㉜缺 c）。
var mutating = map[string]bool{
	"POST /api/v3/order": true, "POST /fapi/v1/order": true, "POST /dapi/v1/order": true,
	"POST /sapi/v1/capital/withdraw": true, "POST /sapi/v1/asset/transfer": true,
	"POST /sapi/v1/sub-account/transfer": true, "POST /sapi/v1/sub-account/universalTransfer": true,
	"POST /sapi/v1/simple-earn/flexible/subscribe": true,
	"POST /sapi/v1/simple-earn/flexible/redeem":    true,
	"POST /sapi/v1/margin/transfer":                true, "POST /sapi/v1/convert/getQuote": true,
	"POST /fapi/v1/positionSide/dual": true, "POST /fapi/v1/leverage": true,
	"POST /fapi/v1/marginType": true, "DELETE /api/v3/order": true,
	"DELETE /fapi/v1/order": true, "DELETE /dapi/v1/order": true,
}

func isMutating(method, path string) bool {
	if mutating[method+" "+path] {
		return true
	}
	return method == "POST" || method == "PUT" || method == "DELETE" // 读全谱=GET 放行，写族标缺
}

func get(c *http.Client, url string) string {
	resp, err := c.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	return string(b)
}

func main() {
	c := &http.Client{Timeout: 30 * time.Second}
	found := map[string]string{} // method+path → method
	for _, repo := range repos {
		for _, m := range regexp.MustCompile(`"path"\s*:\s*"([^"]+\.md)"`).FindAllStringSubmatch(
			get(c, fmt.Sprintf("https://api.github.com/repos/%s/git/trees/HEAD?recursive=1", repo)), -1) {
			md := get(c, fmt.Sprintf("https://raw.githubusercontent.com/%s/HEAD/%s", repo, m[1]))
			for _, e := range pathRe.FindAllStringSubmatch(md, -1) {
				p := strings.TrimRight(e[2], "`'\" ")
				found[e[1]+" "+p] = e[1]
			}
		}
	}
	keys := make([]string, 0, len(found))
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	var mutatingN int
	for _, k := range keys {
		parts := strings.SplitN(k, " ", 2)
		if isMutating(parts[0], parts[1]) {
			mutatingN++
			out = append(out, "MUTATING\t"+parts[1])
		} else {
			out = append(out, "GET\t"+parts[1])
		}
	}
	os.WriteFile(listOut,
		[]byte("# D-608 生成物——官方 docs GitHub 仓库 markdown 机械枚举（swagger 仅交叉校验）\n"+strings.Join(out, "\n")+"\n"), 0644)
	fmt.Printf("bnprobe gen_list: %d 端点（mutating=%d 标缺）→%s\n", len(keys), mutatingN, listOut)
}
