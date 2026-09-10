package eventwatch

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
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

// fakeStore 实现 alertStore 接口，记录所有 InsertAlert 调用（R1 Poll 级测试用）。
type fakeStore struct {
	existingFragments map[string]bool // 已存在的 fragment（去重查命中=true）
	inserted          []string        // 所有 InsertAlert 的 message
}

func newFakeStore() *fakeStore {
	return &fakeStore{existingFragments: make(map[string]bool)}
}

func (f *fakeStore) AlertExistsByFragment(_ context.Context, fragment string) (bool, error) {
	return f.existingFragments[fragment], nil
}

func (f *fakeStore) InsertAlert(_ context.Context, _ *int64, _, message string) error {
	f.inserted = append(f.inserted, message)
	return nil
}

// 旧 mockStore 保留供历史测试引用（签名与 fakeStore 相同）。
type mockStore = fakeStore

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

// --- R1: Poll 级测试（mock fetch + fake store） ---

// newTestCollector 构造可注入 mock fetch + fake store 的 Collector。
func newTestCollector(store alertStore, hkexFn func(context.Context, *http.Client, string) ([]hkexItem, error), edgarFn func(context.Context, *http.Client, string) ([]edgarEntry, error)) *Collector {
	return &Collector{
		store:      store,
		watchCodes: []string{"00626", "01788"},
		hc:         &http.Client{},
		hkexBase:   "http://test",
		edgarURL:   "http://test",
		hkexFetch:  hkexFn,
		edgarFetch: edgarFn,
	}
}

// R1①：2 条命中 → InsertAlert 2 次
func TestPoll_TwoHits_TwoAlerts(t *testing.T) {
	store := newFakeStore()
	// 用非在跟代码（00700）避免在跟代码定向重复推送
	hkexItems := []hkexItem{
		{NewsID: "2001", StockCode: "00700", Title: "Voluntary Offer - Privatisation", FileLink: "/a/2001.pdf"},
		{NewsID: "2002", StockCode: "00700", Title: "Scheme of Arrangement", FileLink: "/a/2002.pdf"},
	}
	c := newTestCollector(store,
		func(_ context.Context, _ *http.Client, _ string) ([]hkexItem, error) { return hkexItems, nil },
		func(_ context.Context, _ *http.Client, _ string) ([]edgarEntry, error) { return nil, nil },
	)
	c.Poll(context.Background())
	// 2 条关键词命中（Voluntary Offer + Scheme of Arrangement）→ 2 次 InsertAlert
	if len(store.inserted) != 2 {
		t.Fatalf("expected 2 alerts, got %d: %v", len(store.inserted), store.inserted)
	}
	for _, msg := range store.inserted {
		if !strings.HasPrefix(msg, "[事件]") {
			t.Errorf("alert missing [事件] prefix: %q", msg)
		}
	}
}

// R1②：fragment 已存在 → 0 次跳过
func TestPoll_FragmentExists_ZeroAlerts(t *testing.T) {
	store := newFakeStore()
	// 预置已存在 fragment
	store.existingFragments["2001"] = true
	store.existingFragments["2002"] = true
	hkexItems := []hkexItem{
		{NewsID: "2001", StockCode: "00626", Title: "Voluntary Offer", FileLink: "/a/2001.pdf"},
		{NewsID: "2002", StockCode: "00700", Title: "Scheme of Arrangement", FileLink: "/a/2002.pdf"},
	}
	c := newTestCollector(store,
		func(_ context.Context, _ *http.Client, _ string) ([]hkexItem, error) { return hkexItems, nil },
		func(_ context.Context, _ *http.Client, _ string) ([]edgarEntry, error) { return nil, nil },
	)
	c.Poll(context.Background())
	if len(store.inserted) != 0 {
		t.Fatalf("expected 0 alerts (all fragments exist), got %d: %v", len(store.inserted), store.inserted)
	}
}

// R1③：两源全失败累计 36 轮 → 第 36 轮通道 warn 恰 1 次后计数重置
func TestPoll_AllFail_36Rounds_OneWarnThenReset(t *testing.T) {
	store := newFakeStore()
	failHkex := func(_ context.Context, _ *http.Client, _ string) ([]hkexItem, error) {
		return nil, fmt.Errorf("hkex down")
	}
	failEdgar := func(_ context.Context, _ *http.Client, _ string) ([]edgarEntry, error) {
		return nil, fmt.Errorf("edgar down")
	}
	c := newTestCollector(store, failHkex, failEdgar)
	ctx := context.Background()
	// 前 35 轮：全失败但不达阈值 → 0 条告警
	for i := 0; i < 35; i++ {
		c.Poll(ctx)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("after 35 fail rounds, expected 0 alerts, got %d", len(store.inserted))
	}
	if c.failStreak != 35 {
		t.Errorf("after 35 rounds, failStreak=%d, want 35", c.failStreak)
	}
	// 第 36 轮：达到阈值 → 1 条通道 warn
	c.Poll(ctx)
	if len(store.inserted) != 1 {
		t.Fatalf("after 36th fail round, expected 1 warn, got %d: %v", len(store.inserted), store.inserted)
	}
	if !strings.Contains(store.inserted[0], "全失败") {
		t.Errorf("warn should mention 全失败: %q", store.inserted[0])
	}
	// 计数重置
	if c.failStreak != 0 {
		t.Errorf("after warn, failStreak should reset to 0, got %d", c.failStreak)
	}
	// 再 1 轮失败 → failStreak=1，无新告警
	c.Poll(ctx)
	if c.failStreak != 1 {
		t.Errorf("after reset+1 fail, failStreak=%d, want 1", c.failStreak)
	}
	if len(store.inserted) != 1 {
		t.Errorf("after reset+1 fail, expected still 1 alert, got %d", len(store.inserted))
	}
}

// --- R2: 零结果降级测试 ---

// R2①：36 tick 全零 → 第 36 轮 warn 1 次后重置
func TestPoll_AllZero_36Ticks_OneWarnThenReset(t *testing.T) {
	store := newFakeStore()
	emptyHkex := func(_ context.Context, _ *http.Client, _ string) ([]hkexItem, error) {
		return []hkexItem{}, nil // 成功但 0 条
	}
	emptyEdgar := func(_ context.Context, _ *http.Client, _ string) ([]edgarEntry, error) {
		return []edgarEntry{}, nil // 成功但 0 条
	}
	c := newTestCollector(store, emptyHkex, emptyEdgar)
	ctx := context.Background()
	// 前 35 轮：全零但不达阈值 → 0 条告警
	for i := 0; i < 35; i++ {
		c.Poll(ctx)
	}
	if len(store.inserted) != 0 {
		t.Fatalf("after 35 zero rounds, expected 0 alerts, got %d", len(store.inserted))
	}
	if c.zeroStreak != 35 {
		t.Errorf("after 35 zero rounds, zeroStreak=%d, want 35", c.zeroStreak)
	}
	// 第 36 轮：达到阈值 → 1 条零结果 warn
	c.Poll(ctx)
	if len(store.inserted) != 1 {
		t.Fatalf("after 36th zero round, expected 1 warn, got %d: %v", len(store.inserted), store.inserted)
	}
	if !strings.Contains(store.inserted[0], "零结果") {
		t.Errorf("warn should mention 零结果: %q", store.inserted[0])
	}
	// 计数重置
	if c.zeroStreak != 0 {
		t.Errorf("after warn, zeroStreak should reset to 0, got %d", c.zeroStreak)
	}
}

// R2②：任一源有结果 → zeroStreak 重置
func TestPoll_AnyResult_ResetsZeroStreak(t *testing.T) {
	store := newFakeStore()
	// 前 3 轮全零
	emptyHkex := func(_ context.Context, _ *http.Client, _ string) ([]hkexItem, error) {
		return []hkexItem{}, nil
	}
	emptyEdgar := func(_ context.Context, _ *http.Client, _ string) ([]edgarEntry, error) {
		return []edgarEntry{}, nil
	}
	c := newTestCollector(store, emptyHkex, emptyEdgar)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		c.Poll(ctx)
	}
	if c.zeroStreak != 3 {
		t.Fatalf("after 3 zero rounds, zeroStreak=%d, want 3", c.zeroStreak)
	}
	// 第 4 轮：HKEX 有结果（非关键词命中，不产生告警，但 hkexCount>0 → 重置 zeroStreak）
	hkexWithItem := []hkexItem{
		{NewsID: "9999", StockCode: "00700", Title: "Annual Report", FileLink: "/a/9999.pdf"},
	}
	c.hkexFetch = func(_ context.Context, _ *http.Client, _ string) ([]hkexItem, error) {
		return hkexWithItem, nil
	}
	c.Poll(ctx)
	if c.zeroStreak != 0 {
		t.Errorf("after a source has results, zeroStreak should reset to 0, got %d", c.zeroStreak)
	}
	// 无告警产生（Annual Report 不命中关键词，00700 不在在跟代码）
	if len(store.inserted) != 0 {
		t.Errorf("expected 0 alerts for non-keyword non-watch item, got %d", len(store.inserted))
	}
}

// R2③：一源失败一源零结果 → 不累加 zeroStreak（非"两源都成功"）
func TestPoll_OneFailOneZero_NoZeroStreak(t *testing.T) {
	store := newFakeStore()
	failHkex := func(_ context.Context, _ *http.Client, _ string) ([]hkexItem, error) {
		return nil, fmt.Errorf("hkex down")
	}
	emptyEdgar := func(_ context.Context, _ *http.Client, _ string) ([]edgarEntry, error) {
		return []edgarEntry{}, nil
	}
	c := newTestCollector(store, failHkex, emptyEdgar)
	ctx := context.Background()
	c.Poll(ctx)
	// 一源失败一源零结果 → 既不累加 failStreak（非两源全失败）也不累加 zeroStreak（非两源都成功）
	if c.failStreak != 0 {
		t.Errorf("one fail one zero: failStreak=%d, want 0 (not all failed)", c.failStreak)
	}
	if c.zeroStreak != 0 {
		t.Errorf("one fail one zero: zeroStreak=%d, want 0 (not all succeeded)", c.zeroStreak)
	}
}

// TestParseEDGAR_Latin1 生产缺陷复归（2026-09-10 部署锚抓到）：EDGAR Atom feed 声明
// encoding="ISO-8859-1"，Go xml.Decoder 无 CharsetReader 时报错→EDGAR 源全废。
// 夹具=9-9 真实 curl 响应头形态（F6 规则①：字符串匹配规格贴真实样本）。
func TestParseEDGAR_Latin1(t *testing.T) {
	body := `<?xml version="1.0" encoding="ISO-8859-1" ?>
<feed xmlns="http://www.w3.org/2005/Atom">
<title>Latest Filings - No recent filings</title>
<entry>
<title>SC 13E-3 [amend] CAFE CO, LTD - Amended</title>
<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/1/0001.htm"/>
<id>urn:accession-number:0001140361-26-001234</id>
<updated>2026-09-10T00:00:00Z</updated>
</entry>
</feed>`
	entries, err := parseEDGAR([]byte(body))
	if err != nil {
		t.Fatalf("latin1 parse: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("entries = %d, want 1", len(entries))
	}
	if entries[0].Link == "" {
		t.Errorf("link empty（href 未取到）")
	}
}
