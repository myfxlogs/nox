package analyst

// rate.go 令牌桶限流（ARB_ANALYST_RATE_PER_HOUR，默认 20 问/小时）。
// 超限报 resource_exhausted（service 层转 connect 错误码）。

import (
	"sync"
	"time"
)

// RateLimiter 单用户简单令牌桶（按小时窗口）。
type RateLimiter struct {
	mu     sync.Mutex
	max    int           // 每小时最大请求数
	window time.Duration // 窗口大小（time.Hour）
	tokens []time.Time   // 窗口内请求时间戳
}

// NewRateLimiter 创建限流器。maxPerHour=0 → 不限流（测试用）。
func NewRateLimiter(maxPerHour int) *RateLimiter {
	return &RateLimiter{
		max:    maxPerHour,
		window: time.Hour,
		tokens: nil,
	}
}

// Allow 尝试取一个令牌。超限返回 false（调用方报 resource_exhausted）。
func (r *RateLimiter) Allow() bool {
	if r == nil || r.max <= 0 {
		return true // 不限流
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	// 淘汰窗口外的时间戳
	cutoff := now.Add(-r.window)
	keep := r.tokens[:0]
	for _, t := range r.tokens {
		if t.After(cutoff) {
			keep = append(keep, t)
		}
	}
	r.tokens = keep
	if len(r.tokens) >= r.max {
		return false
	}
	r.tokens = append(r.tokens, now)
	return true
}
