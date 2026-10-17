// Package brokertz —— venue 服务端时区缓存（D-337 S2→审计续段下沉共享）。
// 数据源=mtapi ServerTimezone RPC，实测返回 double offset（小时，含 DST 在效偏移）——
// 设计预授权降级分支：tz 名不可得 → FixedZone(now offset)；offset 随 TTL 24h 重取=DST 修正
// （切换后最迟一个 TTL 自愈，窗口内误差 ≤1h）。tz 名源可用时走 LoadLocation（DST 精确）。
// 禁止硬编码时区。消费面：dashboard 持仓卡 swap 计费零点 + swapledger 三倍日 weekday
// 判定（2026-10-17 时序审计病灶①修：venue 语义 weekday 须按 server 日期取）。
package brokertz

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"
)

// Fetcher venue→ServerTimezone RPC 适配（main 装配自 bm.ServerTZQueriers）。
type Fetcher func(ctx context.Context, venueKey string) (offsetHours float64, err error)

// serverTZRetryCooldown 失败后重试冷却（"失败重试下轮"=下几个轮询周期，防失败 RPC 每秒重放）。
const serverTZRetryCooldown = 5 * time.Minute

// Cache venue server 时区缓存——构造注入值类型（nil 字面量编译红=
// 编译期装配守卫；D-337R1 setter 可忘断链事故根除）。mu 为指针：值类型按值拷贝后
// map 共享底层，锁亦必须共享（b952f491 实证：flush 解锁后并发 dirtyLocked 再挂
// AfterFunc + RPC 快照路径同调 Get → 并发 map 写 fatal）。
// 零值=fetch nil → Get 恒 nil（诚实空态，自防御）。
type Cache struct {
	mu        *sync.Mutex
	ttl       time.Duration
	fetch     Fetcher
	loc       map[string]*time.Location
	fetchedAt map[string]time.Time // 成功=ttl 内命中；失败=冷却计时（retry after cooldown）
}

// New 构造（值返回；map/mu 字段引用语义共享底层；cmd/core 装配点用）。
func New(ttl time.Duration, fetch Fetcher) Cache {
	return Cache{
		mu:        &sync.Mutex{},
		ttl:       ttl,
		fetch:     fetch,
		loc:       map[string]*time.Location{},
		fetchedAt: map[string]time.Time{},
	}
}

// HasFetcher 零值缓存判定（构造必选注入的诚实空态读口——消费方分支用）。
func (c Cache) HasFetcher() bool { return c.fetch != nil && c.mu != nil }

// Get 返回 venue 的 server 时区。miss/过期/冷却结束 → 锁外同步抓取一次（fetch=网络
// 调用不持锁；重复 fetch 竞争良性=同值覆写），成功 TTL 24h，失败 5min 冷却后重试。
// 失败 → nil（诚实空态，组装继续）。
func (c Cache) Get(ctx context.Context, venueKey string) *time.Location {
	if c.fetch == nil || c.mu == nil {
		return nil // 零值缓存（构造必选注入后不可达；自防御诚实空态）
	}
	c.mu.Lock()
	loc, hasLoc := c.loc[venueKey]
	at, hasAt := c.fetchedAt[venueKey]
	if hasLoc && hasAt && time.Since(at) < c.ttl {
		c.mu.Unlock()
		return loc // 成功缓存 TTL 内
	}
	if hasAt && time.Since(at) < serverTZRetryCooldown {
		c.mu.Unlock()
		return loc // 失败/过期冷却中：不再重复阻塞
	}
	c.mu.Unlock()
	off, err := c.fetch(ctx, venueKey)
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
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

// NextSwapChargeAtInLoc 纯函数：now → loc 时区的下一个零点（UTC 时刻返回）。T1 DST 锚（FixedZone 无 DST）。
func NextSwapChargeAtInLoc(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	mid := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	return mid.AddDate(0, 0, 1)
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
