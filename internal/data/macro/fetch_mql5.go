// fetch_mql5.go —— D-528 S2：MQL5 财历隐藏 POST 端点（移植 ark news/fetcher.go，stdlib 重写）。
//
// ⚠️ UA 铁律（2026-09-28 实测，测试钉死）：User-Agent 必须是精简串 "Mozilla/5.0"——
// Chrome 完整模板 UA（Chrome/122.0.0.0 系）→ Angie WAF 确定性 403。
// Referer+X-Requested-With 必设：XHR 面缺失时端点回 200+[]（设计稿自测空数组正因三头全无；
// 修正以 A2「非空」验收锚为准，实测仅补这两头即吐真实事件集）。Origin 仍不设。
//
// 时区（D-053 ①2026-10-04 zh/en 并排对拍钉死）：
//   - FullDate 字段恒为 UTC（ReleaseDate Unix ms 交叉验证，en/zh 两端点一致）；
//   - from/to POST 裸串被站点按服务器本地时区（EET，夏令时 UTC+3）的「日」粒度选择，
//     实际返回窗 = [from 日 −1 天, to 日 +1 天]（UTC 事件坐标）。对拍实证（date_mode=1）：
//     en from=10-02T12:00 to=10-02T13:00 → 返回 min=10-01T12:00 max=10-03T00:00（UTC）
//     en from=10-05T00:00 to=10-05T02:00 → 返回 min=10-04T00:00 max=10-06T00:30（UTC）
//     zh from=10-02T00:00 to=10-03T00:00 → 回 10-01 事件（业主实测复现，两域行为同构）
//     即端点侧无精确时间窗——本函数传值仍用 UTC 格式（语义=大致范围），精确窗过滤一律
//     落库后按 event_time_utc 查询（MacroEventsBetween）；多回的 ±1~2 天事件 upsert 幂等无害。
//
// 失败重试一次（3s 退避，ctx 感知）——禁吞错空跑（KB pagination-freeze 教训）。
package macro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strings"
	"time"

	"nox/internal/store/pgstore"
)

// mql5URL 隐藏 POST 端点（参数化仅为可测；生产传 mql5URL 常量）。
// ①：en→zh——事件名落中文（用户域语言；en 行经迁移 0176 清除防日报双语重复）。
const mql5URL = "https://www.mql5.com/zh/economic-calendar/content"

// mql5UserAgent WAF 白名单 UA（铁律：禁改完整浏览器模板）。
const mql5UserAgent = "Mozilla/5.0"

const (
	mql5ImportanceAll  = "15"     // 位掩码：low(1)+medium(2)+high(4)+holiday(8)
	mql5CurrenciesMask = "131071" // 全主要币种
	mql5DateModeWeek   = "1"      // 自定义 from/to 范围模式（月模式 4/5/6 本票不用，仅拉当周+下周）
)

// mql5Event 上游 JSON 行（字段照 ark 实测结构）。
type mql5Event struct {
	ID               int    `json:"Id"`
	EventName        string `json:"EventName"`
	Importance       string `json:"Importance"`
	CurrencyCode     string `json:"CurrencyCode"`
	Country          int    `json:"Country"`
	ActualValue      string `json:"ActualValue"`
	ForecastValue    string `json:"ForecastValue"`
	PreviousValue    string `json:"PreviousValue"`
	OldPreviousValue string `json:"OldPreviousValue"`
	FullDate         string `json:"FullDate"` // UTC
	Processed        int    `json:"Processed"`
}

// FetchMQL5Calendar 拉 [from,to]（任意时区，内部转 UTC 格式）财历事件。
func FetchMQL5Calendar(ctx context.Context, client *http.Client, endpoint string, from, to time.Time) ([]pgstore.MacroEvent, error) {
	raw, err := fetchMQL5Retry(ctx, client, endpoint, from, to)
	if err != nil {
		return nil, err
	}
	out := make([]pgstore.MacroEvent, 0, len(raw))
	for _, e := range raw {
		t, err := time.ParseInLocation("2006-01-02T15:04:05", e.FullDate, time.UTC)
		if err != nil {
			log.Printf("[macro:mql5] skip event id=%d %s: bad date %q", e.ID, e.EventName, e.FullDate)
			continue
		}
		currency := e.CurrencyCode
		if currency == "" {
			currency = countryIDToCurrency(e.Country)
		}
		if currency == "" {
			continue
		}
		imp := strings.ToLower(e.Importance)
		// ④：MQL5 把假日标为 Importance="none"（实拍 21 行：CNY 国庆/EUR 统一日…）——
		// none ∧ forecast/previous/actual 全空 → 归 holiday（落库归一，raw 不改）；
		// report.go importanceCN["holiday"] 假期段随之命中。
		if imp == "none" && e.ForecastValue == "" && e.PreviousValue == "" && e.ActualValue == "" {
			imp = "holiday"
		}
		rawJSON, _ := json.Marshal(e)
		out = append(out, pgstore.MacroEvent{
			Source:     "mql5",
			EventTime:  t.UTC(),
			Currency:   currency,
			Name:       e.EventName,
			Importance: imp,
			Forecast:   e.ForecastValue,
			Previous:   e.PreviousValue,
			Actual:     e.ActualValue,
			Revision:   e.OldPreviousValue,
			Raw:        rawJSON,
		})
	}
	return out, nil
}

// fetchMQL5Retry POST+重试一次（transient 失败 3s 退避）。
func fetchMQL5Retry(ctx context.Context, client *http.Client, endpoint string, from, to time.Time) ([]mql5Event, error) {
	raw, err := doFetchMQL5(ctx, client, endpoint, from, to)
	if err != nil {
		log.Printf("[macro:mql5] fetch failed, retry in 3s: %v", err)
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(3 * time.Second):
		}
		raw, err = doFetchMQL5(ctx, client, endpoint, from, to)
	}
	return raw, err
}

func doFetchMQL5(ctx context.Context, client *http.Client, endpoint string, from, to time.Time) ([]mql5Event, error) {
	form := url.Values{}
	form.Set("date_mode", mql5DateModeWeek)
	form.Set("from", from.UTC().Format("2006-01-02T15:04:05"))
	form.Set("to", to.UTC().Format("2006-01-02T15:04:05"))
	form.Set("importance", mql5ImportanceAll)
	form.Set("currencies", mql5CurrenciesMask)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	// UA 铁律 + XHR 面（见文件头）：Referer/XRW 必设（缺→200 空数组），Origin 禁设。
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json, text/javascript, */*; q=0.01")
	req.Header.Set("User-Agent", mql5UserAgent)
	req.Header.Set("Referer", "https://www.mql5.com/zh/economic-calendar") // 与端点语言域同步（XHR 面）
	req.Header.Set("X-Requested-With", "XMLHttpRequest")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("http: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %.200s", resp.StatusCode, body)
	}
	var events []mql5Event
	if err := json.Unmarshal(body, &events); err != nil {
		return nil, fmt.Errorf("parse: %w (body: %.200s)", err, body)
	}
	return events, nil
}

// countryIDToCurrency MQL5 国家码→币种回退（CurrencyCode 为空时用；照 ark 映射）。
func countryIDToCurrency(countryID int) string {
	switch countryID {
	case 840:
		return "USD"
	case 999, 276, 250, 380, 724, 528, 56, 40, 246, 372, 620, 300, 705, 233, 428, 440, 196, 442, 470:
		return "EUR"
	case 392:
		return "JPY"
	case 826:
		return "GBP"
	case 36:
		return "AUD"
	case 124:
		return "CAD"
	case 756:
		return "CHF"
	case 554:
		return "NZD"
	case 156:
		return "CNY"
	default:
		return ""
	}
}
