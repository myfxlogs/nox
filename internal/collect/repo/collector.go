// D-266 CNY 逆回购利率雷达采集器（S2）。
// 数据源：东方财富 push2 API（上证质押式回购 GC001/GC007/GC014/GC028 实时价）。
// 采集模式：交易时段（北京时间 9:30-15:00 周一至五 + cnfi_calendar 非节假日）每 5 分钟一次。
// 写入 fin_repo_rates 表（4 行/次），告警在 Poll 内判定（零额外 goroutine）。
// SSOT: docs/D-266设计-CNY逆回购利率雷达-2026-09-10-Claude.md §2.1
package repo

import (
	"context"
	"fmt"
	"log"
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
