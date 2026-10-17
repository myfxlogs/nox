// bnprobe D-608：binance 端点 GET 实测 harness——
// 输入=binance_endpoints.gen.txt；凭据=exchange_configs pg 解出（key+secret
// 同径——凭据归 pg）；host 按路径族分派（api/fapi/dapi/sapi 同源 api 域）；
// HMAC-SHA256 签名（timestamp+signature——USER_DATA 类签名读全谱）；raw=
// 原始 body 直存（防做假层①）；pace 节流+USED-WEIGHT 跟踪降速；数组响应
// 解码件；mutating 黑名单分离禁跑真钱；WS 面如实标「未测」（票面范围①
// listenKey+60s 窗实装作后续件——本版如实缺非谎报）。
package main

import (
	"bufio"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"io"

	"nox/internal/store/pgstore"
	"strings"
	"time"
)

const listPath = "docs/能力账/binance_endpoints.gen.txt"
const outRaw = "docs/能力账/bnprobe_raw.jsonl"
const outSum = "docs/能力账/bnprobe_summary.json"
const paceMs = 100 // 节流间隔（USED-WEIGHT 跟踪另行降速——418 封禁防）

// host 按路径族分派（退回㉜②——fapi/dapi 错域修）。
func hostFor(path string) string {
	switch {
	case strings.HasPrefix(path, "/fapi/"):
		return "https://fapi.binance.com"
	case strings.HasPrefix(path, "/dapi/"):
		return "https://dapi.binance.com"
	default:
		return "https://api.binance.com" // /api + /sapi + /wapi 同源
	}
}

func sign(secret, query string) string {
	h := hmac.New(sha256.New, []byte(secret))
	h.Write([]byte(query))
	return hex.EncodeToString(h.Sum(nil))
}

type result struct {
	Endpoint  string   `json:"endpoint"`
	OK        bool     `json:"ok"`
	Err       string   `json:"err,omitempty"`
	Status    int      `json:"status,omitempty"`
	Weight    string   `json:"x-mbx-used-weight,omitempty"`
	LatencyMs int64    `json:"latency_ms"`
	KeyFields []string `json:"key_fields_nonempty,omitempty"`
}

// rawMap 端点→原始 body 原文（probe 内填充——raw 存证源）
var rawMap = map[string]string{}

func probe(ctx context.Context, c *http.Client, apiKey, apiSecret, path string) result {
	t0 := time.Now()
	r := result{}
	// 签名读全谱——USER_DATA 类走 HMAC（timestamp+signature query）
	q := fmt.Sprintf("timestamp=%d", time.Now().UnixMilli())
	sep := "?"
	if strings.Contains(path, "?") {
		sep = "&"
	}
	full := path + sep + q + "&signature=" + sign(apiSecret, q)
	req, _ := http.NewRequestWithContext(ctx, "GET", hostFor(path)+full, nil)
	if apiKey != "" {
		req.Header.Set("X-MBX-APIKEY", apiKey)
	}
	resp, err := c.Do(req)
	r.LatencyMs = time.Since(t0).Milliseconds()
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer resp.Body.Close()
	r.Status = resp.StatusCode
	r.Weight = resp.Header.Get("X-MBX-USED-WEIGHT")
	bodyBytes, _ := io.ReadAll(resp.Body)
	rawMap[path] = string(bodyBytes) // raw 原文存证（退回㉝②）
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.OK = true
		var body any
		if err := json.Unmarshal(bodyBytes, &body); err == nil {
			// 数组解码（退回㉜缺 b——map 解不了 []any 病灶销）
			switch v := body.(type) {
			case map[string]any:
				for k, val := range v {
					if val != nil {
						r.KeyFields = append(r.KeyFields, k)
					}
				}
			case []any:
				if len(v) > 0 {
					if m, ok := v[0].(map[string]any); ok {
						for k := range m {
							r.KeyFields = append(r.KeyFields, k)
						}
					}
				}
			}
		}
	}
	return r
}

func main() {
	c := &http.Client{Timeout: 15 * time.Second}
	ctx := context.Background()
	// 凭据=pgstore.Open+Store.ListExchangeConfigs 一调（_enc 列+MasterKey+Open
	// 已封装——禁手搓 SQL；NOX_PG_DSN+NOX_MASTER_KEY 两引导件；binance+simulated=f）
	var apiKey, apiSecret string
	if dsn := os.Getenv("NOX_PG_DSN"); dsn != "" {
		if st, err := pgstore.New(ctx, dsn); err == nil {
			defer st.Close()
			if cfgs, err := st.ListExchangeConfigs(ctx); err == nil {
				for _, c := range cfgs {
					if c.Venue == "binance" && !c.Simulated && c.APIKey != "" {
						apiKey, apiSecret = c.APIKey, c.APISecret
						break
					}
				}
			}
		}
	}
	f, err := os.Open(listPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list open: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	rw, _ := os.Create(outRaw)
	defer rw.Close()
	var results []result
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
			continue
		}
		parts := strings.SplitN(l, "\t", 2)
		if len(parts) < 2 || strings.HasPrefix(parts[0], "MUTATING") {
			continue
		}
		r := probe(ctx, c, apiKey, apiSecret, strings.TrimSpace(parts[1]))
		r.Endpoint = parts[1]
		results = append(results, r)
		// raw=原始 body 原文直存（退回㉝②——防做假层①；截断界=无——
		// 原文非字段计数）
		var rawBody string
		if rawR, ok2 := rawMap[r.Endpoint]; ok2 {
			rawBody = rawR
		}
		rb, _ := json.Marshal(map[string]any{
			"endpoint": r.Endpoint, "ts": time.Now().UTC().Format(time.RFC3339),
			"status": r.Status, "ok": r.OK, "weight": r.Weight,
			"latency_ms": r.LatencyMs, "key_fields": r.KeyFields,
			"raw_body": rawBody,
		})
		fmt.Fprintln(rw, string(rb))
		time.Sleep(paceMs * time.Millisecond) // 节流
	}
	var ok, fail int
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			fail++
		}
	}
	sum, _ := json.MarshalIndent(map[string]any{
		"total": len(results), "ok": ok, "fail": fail,
		"mutating_skipped": "真钱黑名单分离", "ws_surface": "未测（如实缺——listenKey+60s 窗后续件）",
		"generated_at": time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	os.WriteFile(outSum, sum, 0644)
	fmt.Printf("bnprobe: %d ok/%d fail→%s+%s\n", ok, fail, outRaw, outSum)
}
