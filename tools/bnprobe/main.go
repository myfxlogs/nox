// bnprobe D-608 退回㉛①：binance 端点 GET 实测 harness——
// 输入=docs/能力账/binance_endpoints.gen.txt（机械生成物）；
// 凭据=app_secrets binance 径（SG 侧经既有 client 装配取——SecretResolver
// 同 exec 凭据链，凭据不落代码不落环境变量明文）；输出=raw+汇总双件。
// 真钱黑名单分离（mutating 不探如实缺）；X-MBX-USED-WEIGHT 权重头采集。
// GET 签名读全谱放行——禁跑真钱端点。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"

	"github.com/jackc/pgx/v5"
	"strings"
	"time"
)

const listPath = "docs/能力账/binance_endpoints.gen.txt"
const outRaw = "docs/能力账/bnprobe_raw.jsonl"
const outSum = "docs/能力账/bnprobe_summary.json"

type result struct {
	Endpoint  string   `json:"endpoint"`
	Tag       string   `json:"tag"`
	OK        bool     `json:"ok"`
	Err       string   `json:"err,omitempty"`
	Status    int      `json:"status,omitempty"`
	Weight    string   `json:"x-mbx-used-weight,omitempty"`
	LatencyMs int64    `json:"latency_ms"`
	KeyFields []string `json:"key_fields_nonempty,omitempty"`
}

func probe(ctx context.Context, c *http.Client, base, apiKey, path string) result {
	t0 := time.Now()
	req, _ := http.NewRequestWithContext(ctx, "GET", base+path, nil)
	if apiKey != "" {
		req.Header.Set("X-MBX-APIKEY", apiKey) // D-608：凭据=exchange_configs 库解出（凭据归 pg 不归 env）
	}
	resp, err := c.Do(req)
	r := result{LatencyMs: time.Since(t0).Milliseconds()}
	if err != nil {
		r.Err = err.Error()
		return r
	}
	defer resp.Body.Close()
	r.Status = resp.StatusCode
	r.Weight = resp.Header.Get("X-MBX-USED-WEIGHT")
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		r.OK = true
		var body map[string]any
		if err := json.NewDecoder(resp.Body).Decode(&body); err == nil {
			for k, v := range body {
				if v != nil && v != "" && v != 0.0 {
					r.KeyFields = append(r.KeyFields, k)
				}
			}
		}
	}
	return r
}

func main() {
	base := "https://api.binance.com"
	// D-608 凭据径=exchange_configs 库读（凭据归 pg——env 凭据直读禁线守；
	// DSN 引导件同 exec 链；SG 侧=同一 pg 实例解密径）。
	dsn := os.Getenv("NOX_PG_DSN")
	var apiKey string
	if dsn != "" {
		if conn, err := pgx.Connect(context.Background(), dsn); err == nil {
			defer conn.Close(context.Background())
			conn.QueryRow(context.Background(),
				`SELECT api_key FROM exchange_configs WHERE venue='binance' AND api_key<>'' LIMIT 1`).Scan(&apiKey)
		}
	}
	c := &http.Client{Timeout: 15 * time.Second}
	f, err := os.Open(listPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "list open: %v\n", err)
		os.Exit(1)
	}
	defer f.Close()
	var results []result
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		l := sc.Text()
		if strings.TrimSpace(l) == "" {
			continue
		}
		parts := strings.SplitN(l, "\t", 2)
		if len(parts) < 2 || strings.HasPrefix(parts[0], "MUTATING") {
			continue // 真钱黑名单分离
		}
		r := probe(context.Background(), c, base, apiKey, strings.TrimSpace(parts[1]))
		r.Endpoint = parts[1]
		r.Tag = parts[0]
		results = append(results, r)
	}
	// raw 行件+汇总件双出
	rw, _ := os.Create(outRaw)
	defer rw.Close()
	for _, r := range results {
		b, _ := json.Marshal(r)
		fmt.Fprintln(rw, string(b))
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
		"mutating_skipped": "真钱黑名单分离", "generated_at": time.Now().UTC().Format(time.RFC3339),
	}, "", "  ")
	os.WriteFile(outSum, sum, 0644)
	fmt.Printf("bnprobe: %d ok/%d fail→%s+%s\n", ok, fail, outRaw, outSum)
}
