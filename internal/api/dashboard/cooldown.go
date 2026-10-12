package dashboard

import (
	"log"
	"sync"
	"time"
)

// Cooldown 账号类周期 RPC 冷却器（D-077 修复⑩ G2，archive #22 事故防御）。
//
// 语义：连续超时 ≥2 → 该 key 10s 冷却跳过、成功即清零（warn-once）。
// 防御场景：4 broker 全降级 25min 事故——离线 venue 周期 RPC 连续超时拖垮整个轮询。
//
// 用法：
//
//	cd := NewCooldown(2, 10*time.Second)
//	if cd.Skip("vA:mt5") { continue } // 冷却中跳过
//	err := rpc()
//	cd.Record("vA:mt5", err)
type Cooldown struct {
	mu            sync.Mutex
	threshold     int                  // 连续超时阈值（≥此值进入冷却）
	cooldownDur   time.Duration        // 冷却时长
	consecutive   map[string]int       // key → 连续超时次数
	coolingUntil  map[string]time.Time // key → 冷却到期时间
	warnedCooling map[string]bool      // key → 是否已 warn-once（进入冷却时）
}

// NewCooldown 创建冷却器。threshold=连续超时阈值，cooldownDur=冷却时长。
func NewCooldown(threshold int, cooldownDur time.Duration) *Cooldown {
	if threshold <= 0 {
		threshold = 2
	}
	if cooldownDur <= 0 {
		cooldownDur = 10 * time.Second
	}
	return &Cooldown{
		threshold:     threshold,
		cooldownDur:   cooldownDur,
		consecutive:   make(map[string]int),
		coolingUntil:  make(map[string]time.Time),
		warnedCooling: make(map[string]bool),
	}
}

// Skip 报告该 key 是否在冷却中（应跳过本次 RPC）。
// 冷却到期自动清除冷却状态（下次调用不再 Skip）。
func (c *Cooldown) Skip(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.coolingUntil[key]
	if !ok {
		return false
	}
	if time.Now().Before(until) {
		if !c.warnedCooling[key] {
			log.Printf("[cooldown] %s cooling, skip (consecutive timeout ≥%d)", key, c.threshold)
			c.warnedCooling[key] = true
		}
		return true
	}
	// 冷却到期：清除冷却+warn 标记（下次进入冷却可再 warn）
	delete(c.coolingUntil, key)
	delete(c.warnedCooling, key)
	return false
}

// Record 记录一次 RPC 结果（err != nil 视为超时/失败，err == nil 清零连续计数）。
// 连续超时 ≥ threshold → 进入冷却（设置 coolingUntil = now + cooldownDur）。
func (c *Cooldown) Record(key string, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err == nil {
		c.consecutive[key] = 0
		return
	}
	c.consecutive[key]++
	if c.consecutive[key] >= c.threshold {
		c.coolingUntil[key] = time.Now().Add(c.cooldownDur)
		// warn-once 在 Skip 时打（避免 Record 也打重复）
	}
}

// Consecutive 返回当前连续超时次数（测试用）。
func (c *Cooldown) Consecutive(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.consecutive[key]
}

// Cooling 返回是否在冷却中（测试用，不含到期清除副作用）。
func (c *Cooldown) Cooling(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	until, ok := c.coolingUntil[key]
	return ok && time.Now().Before(until)
}
