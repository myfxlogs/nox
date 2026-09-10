package eventwatch

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// --- HKEXnews 解析测试 ---

func TestParseHKEX(t *testing.T) {
	// 构造与真实 API 同构的 JSON：result 字段是 JSON 字符串
	items := []hkexItem{
		{NewsID: "1001", StockCode: "00626", Title: "Voluntary Offer - Privatisation", FileLink: "/listedco/sehk/20260910/1001.pdf"},
		{NewsID: "1002", StockCode: "02318<br/>05131", Title: "Interim Report 2026", FileLink: "/listedco/sehk/20260910/1002.pdf"},
	}
	resultJSON, _ := json.Marshal(items)
	resp := hkexResponse{Result: string(resultJSON), RecordCnt: 2}
	body, _ := json.Marshal(resp)

	parsed, err := parseHKEX(body)
	if err != nil {
		t.Fatalf("parseHKEX: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 items, got %d", len(parsed))
	}
	if parsed[0].NewsID != "1001" {
		t.Errorf("first item NewsID: got %q, want 1001", parsed[0].NewsID)
	}
	if parsed[1].StockCode != "02318<br/>05131" {
		t.Errorf("multi-code field: got %q", parsed[1].StockCode)
	}
}

func TestParseHKEXNullResult(t *testing.T) {
	body := []byte(`{"result":"null","hasNextRow":false,"recordCnt":0}`)
	parsed, err := parseHKEX(body)
	if err != nil {
		t.Fatalf("parseHKEX null: %v", err)
	}
	if len(parsed) != 0 {
		t.Fatalf("expected 0 items for null result, got %d", len(parsed))
	}
}

// --- EDGAR Atom 解析测试 ---

func TestParseEDGAR(t *testing.T) {
	atomXML := `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <entry>
    <title>ABC Corp - SC 13E-3 filing</title>
    <id>https://www.sec.gov/Archives/edgar/data/123/13e3-123.htm</id>
    <updated>2026-09-10T10:00:00-04:00</updated>
  </entry>
  <entry>
    <title>XYZ Inc - SC 13E-3 going private</title>
    <id>https://www.sec.gov/Archives/edgar/data/456/13e3-456.htm</id>
    <updated>2026-09-10T11:00:00-04:00</updated>
  </entry>
</feed>`
	parsed, err := parseEDGAR([]byte(atomXML))
	if err != nil {
		t.Fatalf("parseEDGAR: %v", err)
	}
	if len(parsed) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(parsed))
	}
	if parsed[0].Title != "ABC Corp - SC 13E-3 filing" {
		t.Errorf("first entry title: got %q", parsed[0].Title)
	}
	// link 回退到 id（EDGAR Atom link 无文本内容）
	if parsed[0].Link != parsed[0].ID {
		t.Errorf("link fallback to id: got link=%q id=%q", parsed[0].Link, parsed[0].ID)
	}
}

func TestParseEDGAREmpty(t *testing.T) {
	atomXML := `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <title>Latest Filings - No recent filings</title>
  <id>https://www.sec.gov/cgi-bin/browse-edgar</id>
</feed>`
	parsed, err := parseEDGAR([]byte(atomXML))
	if err != nil {
		t.Fatalf("parseEDGAR empty: %v", err)
	}
	if len(parsed) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(parsed))
	}
}

// --- 关键词过滤测试 ---

func TestMatchKeyword(t *testing.T) {
	positive := []string{
		"Voluntary Offer - Privatisation",
		"私有化计划文件",
		"协议安排通知",
		"要约收购公告",
		"Scheme of Arrangement",
		"Going Private Transaction",
		"Tender Offer for Shares",
	}
	for _, title := range positive {
		if !matchKeyword(title) {
			t.Errorf("should match keyword: %q", title)
		}
	}
	negative := []string{
		"Interim Report 2026",
		"Annual Report",
		"ESG Report",
		"Monthly Return",
		"Notice of General Meeting",
	}
	for _, title := range negative {
		if matchKeyword(title) {
			t.Errorf("should NOT match keyword: %q", title)
		}
	}
}

func TestMatchWatchCode(t *testing.T) {
	watchCodes := []string{"00626", "01788"}
	tests := []struct {
		field string
		want  bool
	}{
		{"00626", true},
		{"01788", true},
		{"00626<br/>02318", true},
		{"02318<br/>00626", true},
		{"02318", false},
		{"006261", false},
		{"", false},
	}
	for _, tt := range tests {
		got := matchWatchCode(tt.field, watchCodes)
		if got != tt.want {
			t.Errorf("matchWatchCode(%q) = %v, want %v", tt.field, got, tt.want)
		}
	}
}

// --- 告警文案格式测试 ---

func TestFormatHKEX(t *testing.T) {
	item := hkexItem{
		NewsID:    "1001",
		StockCode: "00626",
		Title:     "Voluntary Offer",
		FileLink:  "/listedco/sehk/20260910/1001.pdf",
	}
	msg := formatHKEX(item, false)
	if !strings.HasPrefix(msg, "[事件] HKEXnews") {
		t.Errorf("missing [事件] prefix: %q", msg)
	}
	if !strings.Contains(msg, "00626.HK") {
		t.Errorf("missing stock code: %q", msg)
	}
	if !strings.Contains(msg, "https://www1.hkexnews.hk/listedco/sehk/20260910/1001.pdf") {
		t.Errorf("missing full link: %q", msg)
	}

	// watch=true 标注 [在跟]
	msgWatch := formatHKEX(item, true)
	if !strings.Contains(msgWatch, "[在跟]") {
		t.Errorf("missing [在跟] tag: %q", msgWatch)
	}
}

func TestFormatEDGAR(t *testing.T) {
	e := edgarEntry{
		Title: "ABC Corp - SC 13E-3",
		ID:    "https://www.sec.gov/filing/123",
	}
	msg := formatEDGAR(e)
	if !strings.HasPrefix(msg, "[事件] SEC EDGAR SC 13E-3") {
		t.Errorf("missing prefix: %q", msg)
	}
	if !strings.Contains(msg, "ABC Corp - SC 13E-3") {
		t.Errorf("missing title: %q", msg)
	}
	if !strings.Contains(msg, "https://www.sec.gov/filing/123") {
		t.Errorf("missing link: %q", msg)
	}
}

// --- 去重测试（mock store） ---

type mockStore struct {
	existingFragments map[string]bool
	inserted          []string
}

func (m *mockStore) AlertExistsByFragment(ctx context.Context, fragment string) (bool, error) {
	return m.existingFragments[fragment], nil
}

func (m *mockStore) InsertAlert(ctx context.Context, ruleID *int64, level, message string) error {
	for frag := range m.existingFragments {
		if strings.Contains(message, frag) {
			m.existingFragments[frag] = true
			m.inserted = append(m.inserted, message)
			return nil
		}
	}
	// 新 fragment：取 message 中第一个 [事件] 行的标识作为 key
	m.existingFragments[message] = true
	m.inserted = append(m.inserted, message)
	return nil
}

// --- 失效降级测试（mock server 返回 500） ---

func TestFetchHKEX_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	hc := &http.Client{}
	_, err := fetchHKEX(context.Background(), hc, srv.URL)
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status 500: %v", err)
	}
}

func TestFetchEDGAR_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()
	hc := &http.Client{}
	_, err := fetchEDGAR(context.Background(), hc, srv.URL+"/edgar")
	if err == nil {
		t.Fatal("expected error on HTTP 500")
	}
}

// --- EDGAR XML 结构验证 ---

func TestEDGARFeedXMLStructure(t *testing.T) {
	// 验证 edgarFeed 结构能正确解析带 href 属性的 link
	atomXML := `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom">
  <link rel="alternate" href="https://www.sec.gov/cgi-bin/browse-edgar"/>
  <entry>
    <title>Test Filing</title>
    <link href="https://www.sec.gov/Archives/edgar/data/123/13e3.xml"/>
    <id>https://www.sec.gov/cgi-bin/browse-edgar?CIK=123</id>
    <updated>2026-09-10T10:00:00-04:00</updated>
  </entry>
</feed>`
	var feed edgarFeed
	if err := xml.Unmarshal([]byte(atomXML), &feed); err != nil {
		t.Fatalf("xml unmarshal: %v", err)
	}
	if len(feed.Entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(feed.Entries))
	}
	// entry 内 link 标签的文本内容为空（href 是属性），所以 Link 字段为空
	if feed.Entries[0].Link != "" {
		t.Errorf("entry link text should be empty for href-only link, got %q", feed.Entries[0].Link)
	}
}
