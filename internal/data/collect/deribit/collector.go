package deribit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"arb/internal/data/fact"
)

// DefaultBaseURL Deribit public API。
const DefaultBaseURL = "https://www.deribit.com"

// DefaultInterval 默认轮询间隔（60s，文档声明 ~20 req/s 富余）。
const DefaultInterval = 60 * time.Second

// DefaultAlertBps 默认告警阈值（15 bps，D-137 §1 依据：taker 0.0003×标的 ≈3bps/腿×4腿≈12bps+滑点）。
const DefaultAlertBps = 15.0

// DefaultExpiryCount 默认采集最近到期日数。
const DefaultExpiryCount = 2

// DefaultFactTTL facts 清理保留时长（7 天，D-137 §4 表体积对策）。
const DefaultFactTTL = 7 * 24 * time.Hour

// FactPurger facts 清理接口（pgstore.Store 满足）。
type FactPurger interface {
	PurgeOptParityFacts(ctx context.Context, maxAge time.Duration) (int64, error)
}

// Collector Deribit 期权平价偏离采集器（实现 collect.Collector）。
// 只采集+监控+告警，不执行/不接账户/免 key。fail-closed：采集失败计数告警。
type Collector struct {
	BaseURL     string
	Client      *http.Client
	Bases       []string // BTC/ETH
	ExpiryCount int
	AlertBps    float64
	AlertFn     AlertFn          // 告警回调（main 注入 store.InsertAlert）
	Purger      FactPurger       // facts 清理（main 注入 pgstore.Store）
	Now         func() time.Time // 可注入时钟（测试用），nil→time.Now

	// fail-closed 健康计数（D-104 模式：连续失败 N 次 warn）
	failCount int

	// facts 清理节流（每日一次）
	lastPurge time.Time

	// 告警规则状态机（per-instrument）
	alertRule *AlertRule
}

// NewCollector 创建采集器（默认值，main 可覆盖字段）。
func NewCollector(alertFn AlertFn, purger FactPurger) *Collector {
	c := &Collector{
		BaseURL:     DefaultBaseURL,
		Client:      &http.Client{Timeout: 15 * time.Second},
		Bases:       []string{"BTC", "ETH"},
		ExpiryCount: DefaultExpiryCount,
		AlertBps:    DefaultAlertBps,
		AlertFn:     alertFn,
		Purger:      purger,
		Now:         time.Now,
	}
	c.alertRule = NewAlertRule(c.AlertBps, alertFn)
	return c
}

// Kind 实现 collect.Collector。
func (c *Collector) Kind() string { return fact.KindOptParityDev }

// nowTime 返回当前时钟（可注入）。
func (c *Collector) nowTime() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// bookSummaryResponse Deribit get_book_summary_by_currency JSON 响应。
type bookSummaryResponse struct {
	Result []rawBookSummary `json:"result"`
}

// rawBookSummary API 原始字段（bid_price/ask_price 可能为 null → 用 *float64）。
type rawBookSummary struct {
	InstrumentName  string   `json:"instrument_name"`
	MarkPrice       float64  `json:"mark_price"`
	UnderlyingPrice float64  `json:"underlying_price"`
	BidPrice        *float64 `json:"bid_price"`
	AskPrice        *float64 `json:"ask_price"`
	BaseCurrency    string   `json:"base_currency"`
}

// Poll 实现 collect.Collector：采集期权+期货快照 → 计算平价偏离 → 产出 Fact + 告警。
// fail-closed：任一请求失败 → 返回 error（Scheduler 退避重试），连续失败计数告警。
func (c *Collector) Poll(ctx context.Context) ([]fact.Fact, error) {
	now := c.nowTime()
	var allFacts []fact.Fact
	var allResults []ParityResult

	for _, base := range c.Bases {
		options, err := c.fetchBookSummary(ctx, base, "option")
		if err != nil {
			c.recordFailure(fmt.Sprintf("option %s: %v", base, err))
			return nil, fmt.Errorf("deribit option %s: %w", base, err)
		}
		futures, err := c.fetchBookSummary(ctx, base, "future")
		if err != nil {
			c.recordFailure(fmt.Sprintf("future %s: %v", base, err))
			return nil, fmt.Errorf("deribit future %s: %w", base, err)
		}
		out := computeAllParity(ParityInput{
			Options:     options,
			Futures:     futures,
			Bases:       []string{base},
			ExpiryCount: c.ExpiryCount,
		}, now)
		allFacts = append(allFacts, out.Facts...)
		allResults = append(allResults, out.Results...)
		if out.Skipped > 0 {
			log.Printf("[deribit] %s: %d pairs skipped (no future)", base, out.Skipped)
		}
	}

	// 成功 → 重置失败计数
	c.failCount = 0

	// 告警评估
	c.alertRule.Threshold = c.AlertBps
	c.alertRule.Evaluate(allResults)

	// facts 清理（每日一次）
	c.maybePurge(ctx)

	return allFacts, nil
}

// fetchBookSummary 拉取 Deribit book_summary（option 或 future）。
func (c *Collector) fetchBookSummary(ctx context.Context, currency, kind string) ([]optBookSummary, error) {
	url := fmt.Sprintf("%s/api/v2/public/get_book_summary_by_currency?currency=%s&kind=%s",
		c.BaseURL, currency, kind)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("http %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var br bookSummaryResponse
	if err := json.Unmarshal(body, &br); err != nil {
		return nil, fmt.Errorf("unmarshal: %w", err)
	}
	out := make([]optBookSummary, 0, len(br.Result))
	for _, r := range br.Result {
		var bid, ask float64
		if r.BidPrice != nil {
			bid = *r.BidPrice
		}
		if r.AskPrice != nil {
			ask = *r.AskPrice
		}
		out = append(out, optBookSummary{
			InstrumentName:  r.InstrumentName,
			MarkPrice:       r.MarkPrice,
			UnderlyingPrice: r.UnderlyingPrice,
			BidPrice:        bid,
			AskPrice:        ask,
			BaseCurrency:    r.BaseCurrency,
		})
	}
	return out, nil
}

// recordFailure fail-closed 健康计数（连续失败 N 次 warn，D-104 模式）。
func (c *Collector) recordFailure(detail string) {
	c.failCount++
	if c.failCount >= 3 && c.AlertFn != nil {
		c.AlertFn("warn", fmt.Sprintf("deribit 采集连续 %d 次失败：%s", c.failCount, detail))
	}
	log.Printf("[deribit] collect failed (%d): %s", c.failCount, detail)
}

// maybePurge 每日一次清理过期 opt_parity_dev facts（7 天 TTL）。
func (c *Collector) maybePurge(ctx context.Context) {
	if c.Purger == nil {
		return
	}
	now := c.nowTime()
	if !c.lastPurge.IsZero() && now.Sub(c.lastPurge) < 24*time.Hour {
		return
	}
	c.lastPurge = now
	n, err := c.Purger.PurgeOptParityFacts(ctx, DefaultFactTTL)
	if err != nil {
		log.Printf("[deribit] purge facts: %v", err)
		return
	}
	if n > 0 {
		log.Printf("[deribit] purged %d opt_parity_dev facts older than %s", n, DefaultFactTTL)
	}
}
