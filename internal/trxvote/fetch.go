// D-269 S2：TronScan HTTP fetch（独立于 internal/collect/trx，v1.1 A1/A2 修复）。
// TRON API JSON shape 是公开事实不是代码逻辑，自建 fetch+struct ~20 行。
package trxvote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// tronscanWitnessJSON 对应 TronScan /api/witness 单行（与 internal/collect/trx/types.go:162 同 shape，独立定义）。
type tronscanWitnessJSON struct {
	Address        string `json:"address"`
	Name           string `json:"name"`
	AnnualizedRate string `json:"annualizedRate"` // 年化 %（字符串，实测 "3.111945"）
	Brokerage      int    `json:"brokerage"`
	RealTimeVotes  int64  `json:"realTimeVotes"`
	Votes          int64  `json:"votes"`
	Producer       bool   `json:"producer"`
	ProducedTotal  int64  `json:"producedTotal"`
}

// httpClient 可被测试替换（httptest.NewServer + SetHTTPClient）。
var httpClient = &http.Client{Timeout: 30 * time.Second}

// SetHTTPClient 替换默认 HTTP 客户端（测试用 httptest.Server）。
func SetHTTPClient(c *http.Client) { httpClient = c }

// tronscanGet 自建 HTTP GET（~10 行，与 internal/collect/trx/apr.go:246 同逻辑但独立）。
func tronscanGet(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("Accept", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("tronscan GET %s: status %d", url, resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// fetchSRList 拉 TronScan /api/witness → 解析 → 转 []SRInfo（D-269 §2.3 v1.1）。
func fetchSRList(ctx context.Context, base string) ([]SRInfo, error) {
	body, err := tronscanGet(ctx, base+"/api/witness")
	if err != nil {
		return nil, fmt.Errorf("fetch witness: %w", err)
	}
	var rows []tronscanWitnessJSON
	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("parse witness: %w", err)
	}
	out := make([]SRInfo, 0, len(rows))
	for i, r := range rows {
		rate, perr := f64z(r.AnnualizedRate)
		if perr != nil {
			return nil, fmt.Errorf("parse %s=%q: %w", "r.AnnualizedRate", r.AnnualizedRate, perr)
		} // 字符串→float，解析失败=0
		out = append(out, SRInfo{
			Address:        r.Address,
			Name:           r.Name,
			AnnualizedRate: rate,
			Brokerage:      r.Brokerage,
			RealTimeVotes:  r.RealTimeVotes,
			Producer:       r.Producer,
			Rank:           i + 1, // TronScan 返回按得票降序，rank=index+1
		})
	}
	return out, nil
}
