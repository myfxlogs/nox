package eventwatch

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
)

// edgarEntry 对应 SEC EDGAR Atom feed 中的单个 entry。
type edgarEntry struct {
	Title   string `xml:"title"`
	Link    string `xml:"link"` // <link> 标签文本内容（部分 feed 用 href 属性）
	ID      string `xml:"id"`
	Updated string `xml:"updated"`
}

// edgarFeed 对应 EDGAR Atom XML 外层结构。
type edgarFeed struct {
	XMLName xml.Name     `xml:"feed"`
	Entries []edgarEntry `xml:"entry"`
	Links   []atomLink   `xml:"link"` // feed 级 link（含 href 属性）
}

// atomLink 对应 Atom <link href="..."/> 元素。
type atomLink struct {
	XMLName xml.Name `xml:"link"`
	Href    string   `xml:"href,attr"`
	Rel     string   `xml:"rel,attr"`
}

// fetchEDGAR 从 SEC EDGAR 获取最新 SC 13E-3 filing 列表（Atom XML）。
// SEC 强制要求标识 UA（S0 侦察确认）。
func fetchEDGAR(ctx context.Context, hc *http.Client, edgarURL string) ([]edgarEntry, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", edgarURL, nil)
	if err != nil {
		return nil, fmt.Errorf("edgar request: %w", err)
	}
	req.Header.Set("User-Agent", uaHeader)
	resp, err := hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("edgar fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("edgar http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("edgar read: %w", err)
	}
	return parseEDGAR(body)
}

// parseEDGAR 解析 EDGAR Atom XML feed。
// 无 filing 时 feed 无 entry 元素（S0 实证：返回空 feed + "No recent filings" 标题）。
func parseEDGAR(body []byte) ([]edgarEntry, error) {
	var feed edgarFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("edgar unmarshal: %w", err)
	}
	// entry 内 <link> 标签：优先取 href 属性，回退到文本内容
	for i := range feed.Entries {
		if feed.Entries[i].Link == "" {
			// 尝试从 link 元素提取 href（XML 解析中 link 字段只取文本，需补充解析）
			// 实际 EDGAR Atom 的 <link> 是空标签带 href 属性，文本内容为空。
			// 此处 link 字段为空时回退到 id（EDGAR entry id = filing URL）。
			if feed.Entries[i].ID != "" {
				feed.Entries[i].Link = feed.Entries[i].ID
			}
		}
	}
	return feed.Entries, nil
}
