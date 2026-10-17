// broker_tz.go D-337 S2：venue server 时区缓存 + swap 计费时刻换算。
// 2026-10-17 时序审计续段：实现下沉 internal/brokertz（swapledger 三倍日 weekday
// 判定共享同一 cache 语义——依赖方向：领域层不得 import dashboard API 面）。
// 本文件仅存类型别名与转发（dashboard 包内引用面零改动）。
package dashboard

import (
	"time"

	"nox/internal/brokertz"
)

// ServerTZFetcher venue→ServerTimezone RPC 适配（main 装配自 bm.ServerTZQueriers）。
type ServerTZFetcher = brokertz.Fetcher

// ServerTZCache venue server 时区缓存（构造注入值类型，语义见 brokertz.Cache）。
type ServerTZCache = brokertz.Cache

// NewServerTZCache 构造转发。
func NewServerTZCache(ttl time.Duration, fetch ServerTZFetcher) ServerTZCache {
	return brokertz.New(ttl, fetch)
}

// nextSwapChargeAtInLoc 纯函数转发（MT 腿 swap 计费零点，server 时区）。
func nextSwapChargeAtInLoc(now time.Time, loc *time.Location) time.Time {
	return brokertz.NextSwapChargeAtInLoc(now, loc)
}

// NextUTCBoundaryHours D-337R1 S2 转发（cmd/core wire_income 消费）。
func NextUTCBoundaryHours(now time.Time, hours int) time.Time {
	return brokertz.NextUTCBoundaryHours(now, hours)
}

// NextFundingSettlementUTC D-337R1 S2 转发（CEX 腿 funding 结算边界）。
func NextFundingSettlementUTC(now time.Time, periodsPerDay int) time.Time {
	return brokertz.NextFundingSettlementUTC(now, periodsPerDay)
}
