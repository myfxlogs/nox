package eventwatch

import (
	"bytes"
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

// latin1ToUTF8 ISO-8859-1 → UTF-8（Latin-1 每字节直接映射 Unicode 码点，转换平凡）。
// EDGAR Atom feed 声明 encoding="ISO-8859-1"（生产实测 2026-09-10），Go xml.Decoder
// 对非 UTF-8 声明要求 CharsetReader——此处零依赖实现（golang.org/x/net/charset 引入违反零新依赖约束）。
func latin1ToUTF8(charset string, input io.Reader) (io.Reader, error) {
	if charset == "ISO-8859-1" || charset == "latin1" {
		body, err := io.ReadAll(input)
		if err != nil {
			return nil, err
		}
		runes := make([]rune, len(body))
		for i, b := range body {
			runes[i] = rune(b)
		}
		return bytes.NewReader([]byte(string(runes))), nil
	}
	return input, nil // 其他声明原样交 Go 默认（UTF-8）
}

// parseEDGAR 解析 EDGAR Atom XML feed。
// 无 filing 时 feed 无 entry 元素（S0 实证：返回空 feed + "No recent filings" 标题）。
func parseEDGAR(body []byte) ([]edgarEntry, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.CharsetReader = latin1ToUTF8
	var feed edgarFeed
	if err := dec.Decode(&feed); err != nil {
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
