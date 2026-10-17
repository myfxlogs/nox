// gen_list D-608：binance 端点清单三源并集机械生成（退回㉝③实装）——
// 源① binance-api-postman .json collections 解析（domain 分组机器可读）
// 源② docs GitHub 仓库 .md 遍历；源③ connector .go/.py 端点常量扫；
// 并集表带「三源各产数/单源独有行/并集总数」对账段——生成物禁手写。
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
)

const listOut = "docs/能力账/binance_endpoints.gen.txt"

var mdRepos = []string{
	"binance/binance-spot-api-docs",
	"binance/binance-futures-connector-go",
	"binance/binance-connector-python",
}
var postmanRepo = "binance/binance-api-postman"
var connRepos = []string{"binance/binance-connector-python", "binance/binance-connector-go"}

var pathRe = regexp.MustCompile(`(GET|POST|PUT|DELETE)\s+(/(api|fapi|dapi|sapi|wapi)/[a-zA-Z0-9_\-/.{}]+)`)
var strRe = regexp.MustCompile(`["'` + "`" + `](/(api|fapi|dapi|sapi|wapi)/[a-zA-Z0-9_\-/.{}]+)["'` + "`" + `]`)

var mutating = map[string]bool{
	"POST /api/v3/order": true, "POST /fapi/v1/order": true, "POST /dapi/v1/order": true,
	"POST /sapi/v1/capital/withdraw": true, "POST /sapi/v1/asset/transfer": true,
	"POST /sapi/v1/sub-account/transfer": true, "POST /sapi/v1/sub-account/universalTransfer": true,
	"POST /sapi/v1/simple-earn/flexible/subscribe": true,
	"POST /sapi/v1/simple-earn/flexible/redeem":    true,
}

func isMutating(method, path string) bool {
	return mutating[method+" "+path] || method == "POST" || method == "PUT" || method == "DELETE"
}

func get(c *http.Client, url string) string {
	resp, err := c.Get(url)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	return string(b)
}

func ghTree(c *http.Client, repo, suffix string) []string {
	var out []string
	for _, m := range regexp.MustCompile(`"path"\s*:\s*"([^"]+`+suffix+`)"`).FindAllStringSubmatch(
		get(c, fmt.Sprintf("https://api.github.com/repos/%s/git/trees/HEAD?recursive=1", repo)), -1) {
		out = append(out, m[1])
	}
	return out
}

func main() {
	c := &http.Client{Timeout: 30 * time.Second}
	src := map[string]map[string]bool{"postman": {}, "md": {}, "conn": {}}
	// 源①postman collections .json（url.path 段拼路径）
	for _, p := range ghTree(c, postmanRepo, `\.json`) {
		body := get(c, fmt.Sprintf("https://raw.githubusercontent.com/%s/HEAD/%s", postmanRepo, p))
		var col map[string]any
		if json.Unmarshal([]byte(body), &col) != nil {
			continue
		}
		var walk func(n any)
		walk = func(n any) {
			if m, ok := n.(map[string]any); ok {
				if u, ok2 := m["url"].(map[string]any); ok2 {
					if segs, ok3 := u["path"].([]any); ok3 {
						var parts []string
						for _, s := range segs {
							parts = append(parts, fmt.Sprint(s))
						}
						pp := "/" + strings.Trim(strings.Join(parts, "/"), "/")
						if strings.HasPrefix(pp, "/api/") || strings.HasPrefix(pp, "/sapi/") ||
							strings.HasPrefix(pp, "/fapi/") || strings.HasPrefix(pp, "/dapi/") || strings.HasPrefix(pp, "/wapi/") {
							method, _ := m["method"].(string)
							src["postman"][method+" "+pp] = true
						}
					}
				}
				for _, v := range m {
					walk(v)
				}
			}
			if a, ok := n.([]any); ok {
				for _, v := range a {
					walk(v)
				}
			}
		}
		walk(col["item"])
	}
	// 源②docs .md 遍历
	for _, repo := range mdRepos {
		for _, p := range ghTree(c, repo, `\.md`) {
			for _, e := range pathRe.FindAllStringSubmatch(get(c,
				fmt.Sprintf("https://raw.githubusercontent.com/%s/HEAD/%s", repo, p)), -1) {
				src["md"][e[1]+" "+strings.TrimRight(e[2], "`'\" ")] = true
			}
		}
	}
	// 源③connector .go/.py 端点常量扫（字面量路径——method 推 GET）
	for _, repo := range connRepos {
		for _, suf := range []string{`\.go`, `\.py`} {
			for _, p := range ghTree(c, repo, suf) {
				for _, e := range strRe.FindAllStringSubmatch(get(c,
					fmt.Sprintf("https://raw.githubusercontent.com/%s/HEAD/%s", repo, p)), -1) {
					src["conn"]["GET "+e[1]] = true
				}
			}
		}
	}
	union := map[string]bool{}
	for _, s := range src {
		for k := range s {
			union[k] = true
		}
	}
	keys := make([]string, 0, len(union))
	for k := range union {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []string
	var mutatingN int
	for _, k := range keys {
		parts := strings.SplitN(k, " ", 2)
		tag := "GET"
		if isMutating(parts[0], parts[1]) {
			mutatingN++
			tag = "MUTATING"
		}
		out = append(out, tag+"\t"+parts[1])
	}
	// 对账段：三源各产数+单源独有行+并集总数
	recon := []string{
		fmt.Sprintf("# 三源对账：postman=%d md=%d conn=%d 并集=%d mutating=%d",
			len(src["postman"]), len(src["md"]), len(src["conn"]), len(keys), mutatingN),
	}
	for _, s := range []string{"postman", "md", "conn"} {
		var own int
		for k := range src[s] {
			if !src["postman"][k] && !src["md"][k] && !src["conn"][k] {
				continue
			}
			only := true
			for _, s2 := range []string{"postman", "md", "conn"} {
				if s2 != s && src[s2][k] {
					only = false
				}
			}
			if only {
				own++
			}
		}
		recon = append(recon, fmt.Sprintf("# %s 单源独有=%d", s, own))
	}
	os.WriteFile(listOut,
		[]byte("# D-608 生成物——三源并集机械枚举（postman+docs md+connector 常量；swagger 仅交叉校验）\n"+
			strings.Join(recon, "\n")+"\n"+strings.Join(out, "\n")+"\n"), 0644)
	fmt.Printf("bnprobe gen_list: 并集 %d 端点（mutating=%d）→%s\n", len(keys), mutatingN, listOut)
}
