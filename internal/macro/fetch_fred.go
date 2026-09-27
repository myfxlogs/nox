// fetch_fred.go —— D-528 S2：FRED observations 采集（移植 ark fred/fetcher.go 白名单子集）。
// FRED_API_KEY 可选：无 key 降级仍可用（上游限流版），空 key 仅 warn 一次（A7）。
package macro

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"sync"
	"time"

	"arb/internal/store/pgstore"
)

// fredURL var=测试 httptest 注入点（生产零改动）。
var fredURL = "https://api.stlouisfed.org/fred/series/observations"

// FredSeries 白名单序列初集（收益率/利差/劳动/通胀/政策利率，设计稿 §S2）。
var FredSeries = []string{"DGS2", "DGS10", "DGS30", "T10Y2Y", "UNRATE", "PAYEMS", "CPIAUCSL", "FEDFUNDS"}

// fredNoKeyWarn 无 key 告警只发一次（进程级）。
var fredNoKeyWarn sync.Once

// fredResp 上游 observations 响应（value="."=缺测跳过）。
type fredResp struct {
	Observations []struct {
		Date  string `json:"date"`
		Value string `json:"value"`
	} `json:"observations"`
}

// FetchFred 拉白名单全部序列最近 limit 条非缺测观测（每序列 limit=10，渲染只需最新值）。
func FetchFred(ctx context.Context, client *http.Client, apiKey string, limit int) ([]pgstore.FredObs, error) {
	if apiKey == "" {
		fredNoKeyWarn.Do(func() {
			log.Printf("[macro:fred] ARB_FRED_API_KEY not set — degraded keyless mode (rate-limited upstream)")
		})
	}
	var out []pgstore.FredObs
	var failed []string
	for _, series := range FredSeries {
		rows, err := fetchFredSeries(ctx, client, series, apiKey, limit)
		if err != nil {
			log.Printf("[macro:fred] series %s failed: %v", series, err)
			failed = append(failed, series)
			continue
		}
		out = append(out, rows...)
	}
	if len(failed) == len(FredSeries) {
		return nil, fmt.Errorf("fred: all %d series failed", len(failed))
	}
	return out, nil
}

func fetchFredSeries(ctx context.Context, client *http.Client, series, apiKey string, limit int) ([]pgstore.FredObs, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fredURL, nil)
	if err != nil {
		return nil, err
	}
	q := req.URL.Query()
	q.Add("series_id", series)
	q.Add("file_type", "json")
	q.Add("limit", strconv.Itoa(limit))
	q.Add("sort_order", "desc")
	if apiKey != "" {
		q.Add("api_key", apiKey)
	}
	req.URL.RawQuery = q.Encode()

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var body fredResp
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, err
	}
	var out []pgstore.FredObs
	for _, o := range body.Observations {
		if o.Value == "." || o.Value == "" {
			continue
		}
		v, err := strconv.ParseFloat(o.Value, 64)
		if err != nil {
			continue
		}
		date, err := time.Parse("2006-01-02", o.Date)
		if err != nil {
			continue
		}
		out = append(out, pgstore.FredObs{SeriesID: series, ObsDate: date, Value: v})
	}
	return out, nil
}
