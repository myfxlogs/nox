// broker_tz.go D-337 S2：venue server 时区缓存 + swap 计费时刻换算。
// 数据源=mtapi ServerTimezone RPC，实测返回 double offset（小时，含 DST 在效偏移）——
// 设计预授权降级分支：tz 名不可得 → FixedZone(now offset)；offset 随 TTL 24h 重取=DST 修正
// （切换后最迟一个 TTL 自愈，窗口内误差 ≤1h）。tz 名源可用时走 LoadLocation（DST 精确，
// 见 nextSwapChargeAtInLoc——T1 DST 锚强制）。禁止硬编码时区。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"time"
)

// ServerTZFetcher D-337 S2：venue→ServerTimezone RPC 适配（main 装配自 bm.ServerTZQueriers）。
type ServerTZFetcher func(ctx context.Context, venueKey string) (offsetHours float64, err error)

// serverTZRetryCooldown D-337：失败后重试冷却（"失败重试下轮"=下几个轮询周期，防失败 RPC 每秒重放）。
const serverTZRetryCooldown = 5 * time.Minute

// ServerTZCache D-337R2 S1：venue server 时区缓存——构造注入值类型（nil 字面量编译红=
// 编译期装配守卫；D-337R1 setter 可忘断链事故根除）。无锁：仅 broker build 单写者协程访问。
// 零值=fetch nil → get 恒 nil（诚实空态，自防御）。
type ServerTZCache struct {
	ttl       time.Duration
	fetch     ServerTZFetcher
	loc       map[string]*time.Location
	fetchedAt map[string]time.Time // 成功=ttl 内命中；失败=冷却计时（retry after cooldown）
}

// NewServerTZCache 构造（值返回；map 字段引用语义共享底层；cmd/core 装配点用）。
func NewServerTZCache(ttl time.Duration, fetch ServerTZFetcher) ServerTZCache {
	return ServerTZCache{
		ttl:       ttl,
		fetch:     fetch,
		loc:       map[string]*time.Location{},
		fetchedAt: map[string]time.Time{},
	}
}

// get 返回 venue 的 server 时区。miss/过期/冷却结束 → 同步抓取一次（每冷却期至多一次
// 阻塞尝试，组装不被反复阻塞；成功 TTL 24h，失败 5min 冷却后重试——"失败重试下轮"）。
// 失败 → nil（诚实空态，组装继续）。
func (c ServerTZCache) get(ctx context.Context, venueKey string) *time.Location {
	if c.fetch == nil {
		return nil // 零值缓存（构造必选注入后不可达；自防御诚实空态）
	}
	loc, hasLoc := c.loc[venueKey]
	at, hasAt := c.fetchedAt[venueKey]
	if hasLoc && hasAt && time.Since(at) < c.ttl {
		return loc // 成功缓存 TTL 内
	}
	if hasAt && time.Since(at) < serverTZRetryCooldown {
		return loc // 失败/过期冷却中：不再重复阻塞
	}
	off, err := c.fetch(ctx, venueKey)
	now := time.Now()
	if err != nil {
		log.Printf("[broker] server timezone %s: %v (cooldown %v)", venueKey, err, serverTZRetryCooldown)
		c.loc[venueKey] = nil
		c.fetchedAt[venueKey] = now
		return nil
	}
	loc = time.FixedZone(fmt.Sprintf("server%+.2f", off), int(off*3600))
	c.loc[venueKey] = loc
	c.fetchedAt[venueKey] = now
	log.Printf("[broker] server timezone %s: offset=%+.2fh cached (ttl=%v)", venueKey, off, c.ttl)
	return loc
}

// nextSwapChargeAtInLoc 纯函数：now → loc 时区的下一个零点（UTC 时刻返回）。T1 DST 锚（LoadLocation 防 DST）。
func nextSwapChargeAtInLoc(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	mid := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return mid.AddDate(0, 0, 1)
}

// nextSwapChargeAtOffset RPC offset 降级路径（ServerTimezone 仅返 double 偏移的实测形态）。
func nextSwapChargeAtOffset(now time.Time, offsetHours float64) time.Time {
	loc := time.FixedZone(fmt.Sprintf("server%+.2f", offsetHours), int(offsetHours*3600))
	return nextSwapChargeAtInLoc(now, loc)
}

// NextUTCBoundaryHours D-337R1 S2 下沉：now（UTC）之后的下一个整 k×hours 边界
// （恰在边界上→取下一个，防双触发）。原 cmd/core nextUTCBoundary 逻辑（income 边界
// 与 CEX funding 结算边界共用，消双份复制）。
func NextUTCBoundaryHours(now time.Time, hours int) time.Time {
	mid := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	step := time.Duration(hours) * time.Hour
	k := int(now.Sub(mid)/step) + 1
	return mid.Add(time.Duration(k) * step)
}

// NextFundingSettlementUTC D-337R1 S2：CEX 腿下次 funding 结算边界（UTC）。
// periodsPerDay 无缓存真值回退 3（8h，D-079 禁静默）——binance 偶发结算时间漂移=显示级误差可忍（设计 §3 边界）。
func NextFundingSettlementUTC(now time.Time, periodsPerDay int) time.Time {
	if periodsPerDay <= 0 {
		periodsPerDay = 3
	}
	return NextUTCBoundaryHours(now, 24/periodsPerDay)
}
