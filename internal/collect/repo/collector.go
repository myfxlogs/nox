// D-266 CNY 逆回购利率雷达采集器（S2）。
// 数据源：东方财富 push2 API（上证质押式回购 GC001/GC007/GC014/GC028 实时价）。
// 采集模式：交易时段（北京时间 9:30-15:00 周一至五 + cnfi_calendar 非节假日）每 5 分钟一次。
// 写入 fin_repo_rates 表（4 行/次），告警在 Poll 内判定（零额外 goroutine）。
// SSOT: docs/D-266设计-CNY逆回购利率雷达-2026-09-10-Claude.md §2.1
package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"time"

	"arb/internal/fact"
	"arb/internal/store/pgstore"
)

// push2API 是东方财富上证回购列表接口（fs=m:1+b:MK0356）。
const push2API = "https://push2.eastmoney.com/api/qt/clist/get?fs=m:1+b:MK0356&fields=f1,f2,f3,f4,f12,f14,f18,f152&pn=1&pz=20&po=1"

// targetCodes 是目标四品种（上证回购，流动性最好）。
var targetCodes = map[string]bool{
	"204001": true, // GC001 1 天期
	"204007": true, // GC007 7 天期
	"204014": true, // GC014 14 天期
	"204028": true, // GC028 28 天期
}

// AlertFn 是告警注入面（main 装配 st.InsertAlert 包装，复用 D-264 飞书链路）。
type AlertFn func(level, msg string) error

// RepoCollector 采集上证回购利率，写 fin_repo_rates + 判定 GC007 spike 告警。
type RepoCollector struct {
	store         *pgstore.Store
	alertFn       AlertFn
	spikeWarn     float64 // GC007 > 此阈值 → warn（默认 3.0%）
	spikeCrit     float64 // GC007 > 此阈值 → critical（默认 5.0%）
	location      *time.Location
	nowFn         func() time.Time
	httpClient    *http.Client
	fetchOverride func(ctx context.Context) ([]byte, error) // 测试注入
}

// NewRepoCollector 创建采集器。spikeWarn/spikeCrit ≤0 时用默认值 3.0/5.0。
func NewRepoCollector(store *pgstore.Store, alertFn AlertFn, spikeWarn, spikeCrit float64) *RepoCollector {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = time.FixedZone("CST", 8*3600) // 容器无 tzdata 兜底
	}
	if spikeWarn <= 0 {
		spikeWarn = 3.0
	}
	if spikeCrit <= 0 {
		spikeCrit = 5.0
	}
	return &RepoCollector{
		store:      store,
		alertFn:    alertFn,
		spikeWarn:  spikeWarn,
		spikeCrit:  spikeCrit,
		location:   loc,
		nowFn:      func() time.Time { return time.Now() },
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

// SetHTTPClient 替换 HTTP 客户端（测试用 httptest.Server）。
func (c *RepoCollector) SetHTTPClient(client *http.Client) { c.httpClient = client }

// SetNowFn 替换当前时间函数（测试注入交易时段/非交易时段）。
func (c *RepoCollector) SetNowFn(fn func() time.Time) { c.nowFn = fn }

// SetFetchOverride 替换 fetch（测试直接注入 JSON，跳过 HTTP）。
func (c *RepoCollector) SetFetchOverride(fn func(ctx context.Context) ([]byte, error)) {
	c.fetchOverride = fn
}

// Kind satisfies collect.Collector.
func (c *RepoCollector) Kind() string { return "repo_rate" }

// Poll satisfies collect.Collector. 逆回购利率不是套利信号，不进 Fact 总线（返回 nil）。
func (c *RepoCollector) Poll(ctx context.Context) ([]fact.Fact, error) {
	// 1. 交易时段判断（北京时间 9:30-15:00，周一至五 + cnfi_calendar 非节假日）
	now := c.nowFn().In(c.location)
	if !c.isTradingTime(ctx, now) {
		return nil, nil
	}
	// 2. GET push2 API
	body, err := c.fetch(ctx)
	if err != nil {
		// 失效诚实：fetch 失败只 log 不告警（设计 §2.1）
		log.Printf("repo: fetch: %v", err)
		return nil, nil
	}
	// 3. 解析 data.diff，过滤目标四品种，写 fin_repo_rates
	rates, err := c.parseAndStore(ctx, body, now)
	if err != nil {
		return nil, fmt.Errorf("repo: parse/store: %w", err)
	}
	// 4. 告警判定（GC007 spike）
	c.checkSpike(ctx, rates)
	return nil, nil
}

// isTradingTime 判断是否在 A 股交易时段（北京时间 9:30-15:00 周一至五 + cnfi_calendar 非节假日）。
// 日历缺失日=保守跳过+log（宁可漏采不可写脏值，设计 §2.1）。
func (c *RepoCollector) isTradingTime(ctx context.Context, now time.Time) bool {
	// 周末跳过
	wd := now.Weekday()
	if wd == time.Saturday || wd == time.Sunday {
		return false
	}
	// 交易时段 9:30-15:00 北京时间
	h, m := now.Hour(), now.Minute()
	clockMin := h*60 + m
	if clockMin < 9*60+30 || clockMin >= 15*60 {
		return false
	}
	// cnfi_calendar 节假日过滤（日历缺失日=保守跳过）
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.location)
	has, err := c.store.CNFICalendarHasDate(ctx, today)
	if err != nil {
		log.Printf("repo: calendar query: %v（日历查询失败，保守跳过）", err)
		return false
	}
	if !has {
		log.Printf("repo: calendar_unknown for %s（保守跳过，F4）", today.Format("2006-01-02"))
		return false
	}
	var business bool
	if err := c.store.Pool().QueryRow(ctx,
		`SELECT is_business_day FROM cnfi_calendar WHERE cal_date=$1`,
		today.Format("2006-01-02")).Scan(&business); err != nil {
		log.Printf("repo: calendar is_business_day query: %v（保守跳过）", err)
		return false
	}
	if !business {
		return false
	}
	return true
}

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

// parseAndStore 解析 JSON，过滤目标四品种，写 fin_repo_rates。
// 脏值防御：f2<=0 或缺字段 → 跳过该品种写入+log（设计 §2.1 R2）。
func (c *RepoCollector) parseAndStore(ctx context.Context, body []byte, now time.Time) ([]RepoRateRow, error) {
	var resp push2Response
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("json unmarshal: %w", err)
	}
	tradeDate := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, c.location)
	var stored []RepoRateRow
	for _, raw := range resp.Data.Diff {
		var item diffItem
		if err := json.Unmarshal(raw, &item); err != nil {
			log.Printf("repo: parse diff item: %v（跳过）", err)
			continue
		}
		if !targetCodes[item.F12] {
			continue // 非目标品种
		}
		// f2/f1 灵活解析：ratePct = f2 / 10^f1（东方财富生产页面 JS 实证，核验依据 §1）
		rateRaw, ok := parseFlexFloat(item.F2)
		if !ok || rateRaw <= 0 {
			// 脏值防御：f2<=0 或缺字段 → 跳过+log（设计 §2.1 R2）
			log.Printf("repo: %s f2 invalid (%s), skip", item.F14, string(item.F2))
			continue
		}
		ratePrec, ok := parseFlexFloat(item.F1)
		if !ok {
			log.Printf("repo: %s f1 missing/invalid (%s), skip", item.F14, string(item.F1))
			continue
		}
		if !validPrecision(ratePrec) {
			log.Printf("repo: %s f1 precision out of range 0..6 (%v), skip", item.F14, ratePrec)
			continue
		}
		ratePct := rateRaw / math.Pow(10, ratePrec)

		// f3/f152 涨跌幅：changePct = f3 / 10^f152（东方财富生产页面 JS 实证）
		// 涨跌幅或其精度缺失/无效 → 保留利率，change_pct 写 NULL（不写假零）
		var changePct *float64
		if changeRaw, ok := parseFlexFloat(item.F3); ok {
			if changePrec, ok := parseFlexFloat(item.F152); ok && validPrecision(changePrec) {
				v := changeRaw / math.Pow(10, changePrec)
				changePct = &v // 合法 f3=0 → &0.0（真实零，非缺失）
			} else {
				log.Printf("repo: %s f152 precision missing/invalid (%s), change_pct=NULL", item.F14, string(item.F152))
			}
		} else {
			log.Printf("repo: %s f3 missing/invalid (%s), change_pct=NULL", item.F14, string(item.F3))
		}

		if err := c.store.InsertRepoRate(ctx, item.F12, item.F14, ratePct, changePct, tradeDate); err != nil {
			return nil, fmt.Errorf("insert %s: %w", item.F14, err)
		}
		stored = append(stored, RepoRateRow{Code: item.F12, Name: item.F14, RatePct: ratePct})
	}
	return stored, nil
}

// RepoRateRow 是已存储的利率行（供告警判定用）。
type RepoRateRow struct {
	Code    string
	Name    string
	RatePct float64
}

// parseFlexFloat 灵活解析 f2/f152：接受数字（int/float）与字符串两形态。
// "-" / null / 缺失 → (0, false)。
func parseFlexFloat(raw json.RawMessage) (float64, bool) {
	if len(raw) == 0 || string(raw) == "null" || string(raw) == `"-"` {
		return 0, false
	}
	// 尝试数字
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, true
	}
	// 尝试字符串数字
	var s string
	if err := json.Unmarshal(raw, &s); err == nil && s != "" && s != "-" {
		var f2 float64
		if _, err := fmt.Sscanf(s, "%f", &f2); err == nil {
			return f2, true
		}
	}
	return 0, false
}

// validPrecision 校验精度参数是否在有效整数范围 0..6。
// 东方财富精度参数为小数位数，超出范围视为脏值不猜测单位。
func validPrecision(p float64) bool {
	if p != math.Trunc(p) { // 非整数
		return false
	}
	return p >= 0 && p <= 6
}

// checkSpike 判定 GC007 spike 告警（warn>spikeWarn / critical>spikeCrit）。
// 去重：同品种同日同阈值只报 1 次（AlreadyAlertedToday 查 alerts 表，pattern 含品种代码）。
func (c *RepoCollector) checkSpike(ctx context.Context, rates []RepoRateRow) {
	if c.alertFn == nil {
		return
	}
	for _, r := range rates {
		if r.Code != "204007" { // 只对 GC007 告警（设计 §2.3.2）
			continue
		}
		level, pattern, msg := "", "", ""
		if r.RatePct > c.spikeCrit {
			level = "critical"
			pattern = "[repo] GC007%critical%"
			msg = fmt.Sprintf("[repo] GC007 年化 %.2f%%，利率飙升窗口（超 critical 阈值 %.1f%%）", r.RatePct, c.spikeCrit)
		} else if r.RatePct > c.spikeWarn {
			level = "warn"
			pattern = "[repo] GC007%建议逆回购%" // R3：warn 独有"建议逆回购"串，critical 用"利率飙升窗口"，互不交叉
			msg = fmt.Sprintf("[repo] GC007 年化 %.2f%%，超过阈值 %.1f%%，建议逆回购", r.RatePct, c.spikeWarn)
		}
		if level == "" {
			continue
		}
		// 去重：同品种同日同阈值只报 1 次
		alerted, err := c.store.AlreadyAlertedToday(ctx, pattern)
		if err != nil {
			log.Printf("repo: dedup check: %v", err)
			continue
		}
		if alerted {
			continue
		}
		if err := c.alertFn(level, msg); err != nil {
			log.Printf("repo: alert %s: %v", level, err)
		}
	}
}
