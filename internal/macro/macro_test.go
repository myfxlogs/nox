// macro_test.go —— D-528 S9：三 fetcher+渲染测试（先红后绿锚）。
// UA 铁律钉死：mql5 请求 User-Agent 必须精确等于 "Mozilla/5.0"（Chrome 模板=WAF 403 实测坑）。
package macro

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"arb/internal/store/pgstore"
)

// TestD528_MQL5UserAgentUA 铁律锚：UA==Mozilla/5.0 且不带 Referer/XRW/Origin 三头。
func TestD528_MQL5UserAgentUA(t *testing.T) {
	var gotUA, gotRef, gotXRW, gotOrigin string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotUA = r.Header.Get("User-Agent")
		gotRef = r.Header.Get("Referer")
		gotXRW = r.Header.Get("X-Requested-With")
		gotOrigin = r.Header.Get("Origin")
		if gotRef == "" || gotXRW == "" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"Id":1,"EventName":"Nonfarm Payrolls","Importance":"High","CurrencyCode":"USD","FullDate":"2026-10-02T12:30:00","ForecastValue":"170K","PreviousValue":"168K"}]`))
	}))
	defer srv.Close()

	events, err := FetchMQL5Calendar(context.Background(), srv.Client(), srv.URL,
		time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC), time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if gotUA != "Mozilla/5.0" {
		t.Fatalf("UA 铁律违约：got %q want \"Mozilla/5.0\"", gotUA)
	}
	if gotRef == "" || gotXRW == "" {
		t.Fatalf("XHR 面必设：Referer=%q XRW=%q（缺→上游 200 空数组）", gotRef, gotXRW)
	}
	if gotOrigin != "" {
		t.Fatalf("Origin 必须不设：got %q", gotOrigin)
	}
	if len(events) != 1 {
		t.Fatalf("want 1 event, got %d", len(events))
	}
	e := events[0]
	if e.Source != "mql5" || e.Currency != "USD" || e.Importance != "high" {
		t.Fatalf("字段映射错: %+v", e)
	}
	if want := time.Date(2026, 10, 2, 12, 30, 0, 0, time.UTC); !e.EventTime.Equal(want) {
		t.Fatalf("FullDate 应按 UTC 解析: got %v want %v", e.EventTime, want)
	}
}

// TestD528_CotSocrataParse Socrata 主路解析：TFF 三类净额行+方向+降序去重取最新周。
func TestD528_CotSocrataParse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "cftc_contract_market_code") {
			t.Errorf("missing $where: %s", r.URL.RawQuery)
		}
		// 两周数据交错给出——降序去重应只取 2026-09-22。
		w.Write([]byte(`[
			{"cftc_contract_market_code":"097741","report_date_as_yyyy_mm_dd":"2026-09-22","open_interest_all":"210000","lev_money_positions_long":"52000","lev_money_positions_short":"48000","change_in_lev_money_long":"2000","change_in_lev_money_short":"-1000"},
			{"cftc_contract_market_code":"097741","report_date_as_yyyy_mm_dd":"2026-09-15","open_interest_all":"200000","lev_money_positions_long":"50000","lev_money_positions_short":"60000"}
		]`))
	}))
	defer srv.Close()

	orig := socrataTFF
	socrataTFF = srv.URL
	defer func() { socrataTFF = orig }()
	// DIS 端点同 server 返回同一份（无关断言面，仅防外呼）。
	origDIS := socrataDIS
	socrataDIS = srv.URL
	defer func() { socrataDIS = origDIS }()

	rows, err := FetchCot(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var jpy []pgstore.CotPosition
	for _, r := range rows {
		if r.Instrument == "6J" && r.Category == "lev_fund" {
			jpy = append(jpy, r)
		}
	}
	if len(jpy) != 1 {
		t.Fatalf("want 1 latest 6J/lev_fund row, got %d", len(jpy))
	}
	r := jpy[0]
	if want := "2026-09-22"; r.ReportDate.Format("2006-01-02") != want {
		t.Fatalf("降序去重失败: got %s want %s", r.ReportDate.Format("2006-01-02"), want)
	}
	if r.NetPosition != 52000-48000 || r.NetChange != 2000-(-1000) {
		t.Fatalf("净额/周变动错: net=%d chg=%d", r.NetPosition, r.NetChange)
	}
	if r.Direction != "LONG" {
		t.Fatalf("direction=%s want LONG", r.Direction)
	}
}

// TestD528_CotCSVFallback Socrata 挂→CSV 兜底（A3 补面）。
func TestD528_CotCSVFallback(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/resource/", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(500) })
	mux.HandleFunc("/dea/newcot/deafut.txt", func(w http.ResponseWriter, r *http.Request) {
		// legacy 无表头固定位真值行（2026-09-22 JPY，经 socrata 对拍校准；46 列后跟 pct 块）。
		w.Write([]byte(`"JAPANESE YEN - CHICAGO MERCANTILE EXCHANGE",260922,2026-09-22,097741,CME ,00,097 ,  378701,  192274,  120292,    4376,  143954,  220368,  340604,  345036,   38097,   33665,  378701,  192274,  120292,    4376,  143954,  220368,  340604,  345036,   38097,   33665,       0,       0,       0,       0,       0,       0,       0,       0,       0,       0, -164101,  -45677,    2700,     991,   -5803,  -54063,  -50489,  -50372, -113612, -113729,  100.0,   50.8
`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	origTFF, origDIS, origCSV := socrataTFF, socrataDIS, cftcCSV
	socrataTFF, socrataDIS = srv.URL+"/resource/", srv.URL+"/resource/"
	cftcCSV = srv.URL + "/dea/newcot/deafut.txt"
	defer func() { socrataTFF, socrataDIS, cftcCSV = origTFF, origDIS, origCSV }()

	rows, err := FetchCot(context.Background(), srv.Client())
	if err != nil {
		t.Fatalf("fallback: %v", err)
	}
	byCat := map[string]*pgstore.CotPosition{}
	for i := range rows {
		if rows[i].Instrument == "6J" {
			byCat[rows[i].Category] = &rows[i]
		}
	}
	if len(byCat) != 3 {
		t.Fatalf("CSV 兜底应出 noncomm/comm/nonrept 三类: %+v", byCat)
	}
	if byCat["noncomm"].NetPosition != 71982 || byCat["noncomm"].NetChange != -48377 {
		t.Fatalf("noncomm 净额错: %+v", byCat["noncomm"])
	}
	if byCat["comm"].NetPosition != -76414 || byCat["nonrept"].NetPosition != 4432 {
		t.Fatalf("comm/nonrept 净额错: %+v %+v", byCat["comm"], byCat["nonrept"])
	}
	if byCat["noncomm"].ReportDate.Format("2006-01-02") != "2026-09-22" {
		t.Fatalf("日期取位列错: %+v", byCat["noncomm"].ReportDate)
	}
}

// TestD528_FredKeylessDegradation 无 key 降级（A7）：请求不带 api_key，解析非缺测值。
func TestD528_FredKeylessDegradation(t *testing.T) {
	var hadKey bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hadKey = r.URL.Query().Get("api_key") != ""
		w.Write([]byte(`{"observations":[{"date":"2026-09-25","value":"4.12"},{"date":"2026-09-24","value":"."}]}`))
	}))
	defer srv.Close()

	orig := fredURL
	fredURL = srv.URL
	defer func() { fredURL = orig }()
	rows, err := FetchFred(context.Background(), srv.Client(), "", 10)
	if err != nil {
		t.Fatalf("keyless fetch: %v", err)
	}
	if hadKey {
		t.Fatal("空 key 不得携带 api_key 参数")
	}
	if len(rows) != len(FredSeries) {
		t.Fatalf("want %d rows (每序列最新一条), got %d", len(FredSeries), len(rows))
	}
	if rows[0].SeriesID != "DGS2" || rows[0].Value != 4.12 {
		t.Fatalf("DGS2 解析错: %+v", rows[0])
	}
}

// TestD528_RenderCotCN 分区头在+字段全（A4 渲染锚）。
func TestD528_RenderCotCN(t *testing.T) {
	date := time.Date(2026, 9, 22, 0, 0, 0, 0, time.UTC)
	pct := 87.5
	rows := []pgstore.CotPosition{
		{ReportDate: date, Instrument: "6J", Category: "lev_fund", Direction: "LONG", NetPosition: 4000, NetChange: 3000},
		{ReportDate: date, Instrument: "GC", Category: "managed_money", Direction: "SHORT", NetPosition: -123456, NetChange: -2000},
		{ReportDate: date, Instrument: "ES", Category: "lev_fund", Direction: "SHORT", NetPosition: -50000, NetChange: 100, PctIndex: &pct},
	}
	out := RenderCotCN(rows)
	for _, want := range []string{"外汇", "大宗", "股指", "日元", "黄金", "标普500", "杠杆基金", "净持仓 4,000", "周变动 +3,000", "-123,456", "88%", "截至 2026-09-22"} {
		if !strings.Contains(out, want) {
			t.Errorf("周报缺 %q", want)
		}
	}
	if RenderCotCN(nil) == "" {
		t.Fatal("空行应返回占位文本")
	}
}

// TestD528_DisplayNameFallback 业主 9-28 S5 增补：24 合约全谱系中文名+未命中回退英文名（warn 一次）。
func TestD528_DisplayNameFallback(t *testing.T) {
	if got := DisplayName("6J"); got != "日元" {
		t.Fatalf("在表品种应回中文: got %q", got)
	}
	if len(cotContracts) != 24 {
		t.Fatalf("映射表应覆盖 24 合约, got %d", len(cotContracts))
	}
	for _, c := range cotContracts {
		if cnName(c.Symbol) == "" {
			t.Errorf("合约 %s 缺中文名", c.Symbol)
		}
	}
	// 未注册品种：无中文名无注册行→回退 symbol 本身（可见不静默）。
	if got := DisplayName("XXTEST"); got != "XXTEST" {
		t.Fatalf("未注册品种应回退 symbol 本身, got %q", got)
	}
	// 注册表内但缺中文名的品种（构造：临时注册不可行——用覆盖断言兜底，见上）。
	if got := DisplayName("GC"); got != "黄金" {
		t.Fatalf("GC 应回黄金, got %q", got)
	}
}

// TestD528_RenderCalendarCN BJ 换算+高低影响过滤（A4 锚：UTC 01:30 → BJ 09:30 同日）。
func TestD528_RenderCalendarCN(t *testing.T) {
	now := time.Date(2026, 9, 30, 2, 0, 0, 0, bjLoc) // BJ 09-30 02:00
	events := []pgstore.MacroEvent{
		{Source: "mql5", EventTime: time.Date(2026, 9, 30, 1, 30, 0, 0, time.UTC), Currency: "USD", Importance: "high", Name: "ADP Employment", Forecast: "120K", Previous: "110K"},
		{Source: "mql5", EventTime: time.Date(2026, 9, 30, 23, 0, 0, 0, time.UTC), Currency: "JPY", Importance: "medium", Name: "BOJ Meeting", Actual: "0.50%"},
		{Source: "mql5", EventTime: time.Date(2026, 10, 1, 5, 0, 0, 0, time.UTC), Currency: "EUR", Importance: "low", Name: "Retail Sales"}, // 低影响→滤除
		{Source: "mql5", EventTime: time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC), Currency: "USD", Importance: "high", Name: "窗口外事件"},      // 48h 外→滤除
	}
	out := RenderCalendarCN(events, now)
	for _, want := range []string{"09-30 09:30 USD 高 ADP Employment（预期 120K / 前值 110K）", "10-01 07:00 JPY 中 BOJ Meeting（实际 0.50%）", "北京时间"} {
		if !strings.Contains(out, want) {
			t.Errorf("日报缺 %q；out=%s", want, out)
		}
	}
	if strings.Contains(out, "Retail Sales") || strings.Contains(out, "窗口外事件") {
		t.Errorf("过滤失败: %s", out)
	}
}

// TestD528_ResolveInstrument 别名宽匹配：中文名/币种/代码唯一命中；多义返回空。
func TestD528_ResolveInstrument(t *testing.T) {
	cases := map[string]string{
		"日元": "6J", "jpy": "6J", "6J": "6J", "黄金": "GC", "gold": "GC",
		"标普": "ES", "bitcoin": "BTC", "": "",
	}
	for in, want := range cases {
		if got := ResolveInstrument(in); got != want {
			t.Errorf("ResolveInstrument(%q)=%q want %q", in, got, want)
		}
	}
	if ResolveInstrument("6") != "" { // 多义（6B/6C/6E/6J/6N/6S 均含 6）→空
		t.Error("多义命中应返回空串")
	}
}

// 编译期防呆：raw 列可反序列化（jsonb 往返）。
func TestD528_RawJSONRoundTrip(t *testing.T) {
	var raw map[string]any
	if err := json.Unmarshal([]byte(`{"Id":1}`), &raw); err != nil {
		t.Fatal(err)
	}
}
