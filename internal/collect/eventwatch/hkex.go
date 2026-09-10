package eventwatch

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// hkexBasePath = HKEXnews 公告搜索 JSON API（S0 侦察确认 GET 可用）。
// 端点：titleSearchServlet.do?lang=EN&category=0&market=SEHK&searchType=0
//
//	&t1code=40000&t2code=40000&datefrom=YYYYMMDD&dateto=YYYYMMDD&row=100
//
// stockCode 参数被服务端忽略 → 客户端按 STOCK_CODE 字段过滤（S0 实证）。
const hkexSearchPath = "/search/titleSearchServlet.do"

// hkexItem 对应 HKEXnews JSON API 返回的单条公告。
type hkexItem struct {
	NewsID    string `json:"NEWS_ID"`
	StockCode string `json:"STOCK_CODE"`
	StockName string `json:"STOCK_NAME"`
	Title     string `json:"TITLE"`
	DateTime  string `json:"DATE_TIME"`
	FileLink  string `json:"FILE_LINK"`
	LongText  string `json:"LONG_TEXT"`
}

// hkexResponse 对应 titleSearchServlet.do JSON 外层结构。
type hkexResponse struct {
	Result    string `json:"result"` // JSON 字符串，内含 []hkexItem
	RecordCnt int    `json:"recordCnt"`
}

// fetchHKEX 从 HKEXnews 获取最近 7 天公告列表（全量，不过滤）。
func fetchHKEX(ctx context.Context, hc *http.Client, base string) ([]hkexItem, error) {
	now := time.Now().UTC()
	from := now.AddDate(0, 0, -7).Format("20060102")
	to := now.Format("20060102")
	params := url.Values{
		"lang":       {"EN"},
		"category":   {"0"},
		"market":     {"SEHK"},
		"searchType": {"0"},
		"t1code":     {"40000"},
		"t2code":     {"40000"},
		"datefrom":   {from},
		"dateto":     {to},
		"row":        {"100"},
	}
	reqURL := base + hkexSearchPath + "?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("hkex request: %w", err)
	}
	req.Header.Set("User-Agent", uaHeader)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("hkex fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("hkex http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("hkex read: %w", err)
	}
	return parseHKEX(body)
}

// parseHKEX 解析 titleSearchServlet.do JSON 响应。
// result 字段是 JSON 字符串（需二次解析），"null"=无结果。
func parseHKEX(body []byte) ([]hkexItem, error) {
	var resp hkexResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("hkex unmarshal outer: %w", err)
	}
	if resp.Result == "null" || resp.Result == "" {
		return nil, nil
	}
	var items []hkexItem
	if err := json.Unmarshal([]byte(resp.Result), &items); err != nil {
		return nil, fmt.Errorf("hkex unmarshal result: %w", err)
	}
	return items, nil
}
