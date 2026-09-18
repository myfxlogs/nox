// D-301a S2：Binance funding 结算周期真值缓存。
// 根因：exchange.go:96,176 调用 fundingPeriodsPerDay(0) 固定回退 3（8h×3），
// exchange_history.go:30 fundingHistoryPeriodsPerDay=3 同理——
// PAXG/XAUT 实为 4h 结算（fapi/v1/fundingInfo fundingIntervalHours=4 实证），
// 全链年化低估 2×。
//
// 修法（方案 A）：boot 时拉一次 fapi/v1/fundingInfo，缓存 symbol→periodsPerDay；
// 实时路径与 history 路径共用该表；查不到→回退 3（沿用 D-079 禁静默纪律）。
package exchange

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"sync"
)

// FundingIntervalCache Binance per-symbol funding 结算周期缓存。
// boot 时 Populate 一次（周期变化极罕见），之后只读；并发安全（RWMutex）。
type FundingIntervalCache struct {
	mu      sync.RWMutex
	periods map[string]int // symbol（如 "PAXGUSDT"）→ periods/day（4h=6, 8h=3）
}

// NewFundingIntervalCache 创建空缓存。
func NewFundingIntervalCache() *FundingIntervalCache {
	return &FundingIntervalCache{periods: make(map[string]int)}
}

// Populate 拉一次 fapi/v1/fundingInfo 并缓存 per-symbol periods/day。
// best-effort：失败只 log 不阻断（调用方继续用回退 3）。
func (c *FundingIntervalCache) Populate(ctx context.Context, cfg Config) error {
	url := fmt.Sprintf("%s/fapi/v1/fundingInfo", cfg.BinanceBaseURL)
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("fundingInfo req: %w", err)
	}
	resp, err := cfg.Client.Do(req)
	if err != nil {
		return fmt.Errorf("fundingInfo fetch: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("fundingInfo http %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("fundingInfo read: %w", err)
	}
	var fi []struct {
		Symbol               string `json:"symbol"`
		FundingIntervalHours any    `json:"fundingIntervalHours"` // 数字或字符串，flexInt 语义
	}
	if err := json.Unmarshal(data, &fi); err != nil {
		return fmt.Errorf("fundingInfo parse: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, f := range fi {
		hours := toFundingIntervalHours(f.FundingIntervalHours)
		if hours <= 0 {
			continue
		}
		c.periods[f.Symbol] = 24 / hours
	}
	log.Printf("[exchange] fundingIntervalCache populated: %d symbols", len(c.periods))
	return nil
}

// PeriodsPerDay 返回 symbol 对应的每日结算次数；缓存未命中→回退 3（D-079 禁静默）。
func (c *FundingIntervalCache) PeriodsPerDay(symbol string) int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if p, ok := c.periods[symbol]; ok && p > 0 {
		return p
	}
	return 3 // 回退 8h×3（D-079 禁静默，调用方计数日志）
}

// toFundingIntervalHours 从 any 提取 fundingIntervalHours（兼容数字/字符串编码）。
func toFundingIntervalHours(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	}
	return 0
}

// Lookup D-334ab S3：带命中位的周期查询——边界计算需区分真值与回退（回退 8h 须 warn 禁静默）。
func (c *FundingIntervalCache) Lookup(symbol string) (int, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.periods[symbol]
	if !ok || p <= 0 {
		return 0, false
	}
	return p, true
}
