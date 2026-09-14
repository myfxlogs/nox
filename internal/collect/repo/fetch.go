// fetch.go 东方财富 push2 API HTTP 请求+响应结构定义。
// 从 collector.go 拆出（D-275 纯重构）。
package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// fetch 发 HTTP GET push2 API。
func (c *RepoCollector) fetch(ctx context.Context) ([]byte, error) {
	if c.fetchOverride != nil {
		return c.fetchOverride(ctx)
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", push2API, nil)
	req.Header.Set("User-Agent", "ARBResearch admin@myfxlogs.org")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("push2 API: status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

// push2Response 是东方财富 push2 API 响应结构。
// data.diff 是 map[string]json.RawMessage（S0 实测：key 为数字字符串 "0".."8"）。
type push2Response struct {
	Data struct {
		Diff map[string]json.RawMessage `json:"diff"`
	} `json:"data"`
}

// diffItem 是 diff map 中每项的结构（f2/f152 可能是数字或字符串，用 json.RawMessage 灵活解析）。
type diffItem struct {
	F12  string          `json:"f12"`  // 代码 "204001"
	F14  string          `json:"f14"`  // 名称 "GC001"
	F1   json.RawMessage `json:"f1"`   // rate 精度参数（ratePct = f2 / 10^f1）
	F2   json.RawMessage `json:"f2"`   // 最新价原始值
	F3   json.RawMessage `json:"f3"`   // 涨跌幅原始值（changePct = f3 / 10^f152）
	F4   json.RawMessage `json:"f4"`   // 精度辅助（保留，暂不参与计算）
	F18  json.RawMessage `json:"f18"`  // 保留扩展位
	F152 json.RawMessage `json:"f152"` // 涨跌幅精度参数
}
