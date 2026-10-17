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

	capabilitypb "nox/internal/gen/capability"
	"nox/internal/store/pgstore"

	"google.golang.org/protobuf/proto"
	"strings"
	"time"
)

const listPath = "docs/能力账/binance_endpoints.gen.txt"

var outPB = "docs/能力账/bn-api-probe-" + time.Now().UTC().Format("2006-01-02") + ".pb"

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

// probe tier 分档：signed=timestamp+signature 直签；public/retry=无签首试——
// signed 档凭据空=fail-closed 标「凭据缺未跑」非产假 401（退回㊳②）。
func probe(ctx context.Context, c *http.Client, apiKey, apiSecret, tier, path string) result {
	t0 := time.Now()
	r := result{}
	var req *http.Request
	var err error
	if tier == "signed" {
		if apiKey == "" || apiSecret == "" {
			r.Err = "凭据缺未跑（signed 档 fail-closed）"
			return r
		}
		q := fmt.Sprintf("timestamp=%d", time.Now().UnixMilli())
		sep := "?"
		if strings.Contains(path, "?") {
			sep = "&"
		}
		full := path + sep + q + "&signature=" + sign(apiSecret, q)
		req, err = http.NewRequestWithContext(ctx, "GET", hostFor(path)+full, nil)
	} else {
		req, err = http.NewRequestWithContext(ctx, "GET", hostFor(path)+path, nil)
	}
	if err != nil {
		r.Err = err.Error()
		return r
	}
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
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		r.Err = err.Error()
		return r
	}
	rawMap[path] = string(bodyBytes) // raw 原文存证（退回㉝②）
	// 退回㊳③：400=缺参自伤族——param-catalogue 喂 symbol=BTCUSDT 重试
	// （真须参≠死端点；400+raw 含 symbol/param 字样=缺参族实证喂参重试）
	if resp.StatusCode == 400 && strings.Contains(string(bodyBytes), "symbol") {
		req2, _ := http.NewRequestWithContext(ctx, "GET", hostFor(path)+path+"?symbol=BTCUSDT", nil)
		if apiKey != "" {
			req2.Header.Set("X-MBX-APIKEY", apiKey)
		}
		if resp2, err2 := c.Do(req2); err2 == nil {
			defer resp2.Body.Close()
			bodyBytes, _ = io.ReadAll(resp2.Body)
			rawMap[path] = string(bodyBytes)
			r.Status = resp2.StatusCode
			r.Weight = resp2.Header.Get("X-MBX-USED-WEIGHT")
			resp = resp2
		}
	}
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
		st, err := pgstore.New(ctx, dsn)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pgstore open: %v（凭据空跑拒——签名面端点标凭据缺）\n", err)
		} else {
			defer st.Close()
			cfgs, err := st.ListExchangeConfigs(ctx)
			if err != nil {
				fmt.Fprintf(os.Stderr, "exchange_configs read: %v\n", err)
			} else {
				for _, c := range cfgs {
					if c.Venue == "binance" && !c.Simulated && c.APIKey != "" {
						apiKey, apiSecret = c.APIKey, c.APISecret
						break
					}
				}
				if apiKey == "" {
					fmt.Fprintln(os.Stderr, "binance 凭据未命中（simulated=f 过滤后空——签名面标凭据缺未跑）")
				}
			}
		}
	} else {
		fmt.Fprintln(os.Stderr, "NOX_PG_DSN 未设——凭据空跑拒（签名面标凭据缺）")
	}
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
		if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "#") {
			continue
		}
		parts := strings.SplitN(l, "\t", 3)
		if len(parts) < 2 || strings.HasPrefix(parts[0], "MUTATING") {
			continue
		}
		tier, path := "public", parts[len(parts)-1]
		if len(parts) == 3 {
			tier = parts[1]
		}
		r := probe(ctx, c, apiKey, apiSecret, tier, path)
		r.Endpoint = parts[1]
		results = append(results, r)
		time.Sleep(paceMs * time.Millisecond) // 节流
	}
	// ③.pb 直出（D-615——ProbeRun 组装+Marshal 写入；raw 原文嵌 raw_bodies map）
	run := &capabilitypb.ProbeRun{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Gateway:     "binance",
		RawBodies:   rawMap,
	}
	for _, r := range results {
		run.Evidence = append(run.Evidence, &capabilitypb.EndpointEvidence{
			Endpoint: r.Endpoint, Ok: r.OK, Status: int32(r.Status),
			Err: r.Err, XMbxUsedWeight: r.Weight, LatencyMs: r.LatencyMs,
			KeyFieldsNonempty: r.KeyFields,
		})
	}
	pbData, err := proto.Marshal(run)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pb marshal: %v\n", err)
		os.Exit(1)
	}
	if err := os.WriteFile(outPB, pbData, 0644); err != nil {
		fmt.Fprintf(os.Stderr, "pb write: %v\n", err)
		os.Exit(1)
	}
	var ok, fail int
	for _, r := range results {
		if r.OK {
			ok++
		} else {
			fail++
		}
	}
	fmt.Printf("bnprobe: %d ok/%d fail→%s\n", ok, fail, outPB)
}
