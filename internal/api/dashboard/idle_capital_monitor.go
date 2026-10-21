// idle_capital_monitor.go —— D-554 闲置资金剪刀差提示（盲区1补洞）。
// 30min tick 实拉 venueAvailability（D-546 R1 60s 缓存件零新增采集）：
// venue 可转可用档合计超阈且显著大于立即可用档 → 转换式 info 告警
// （「trading 池不够而 funding 有钱→组合死资金闸+机会白丢零提示」的主动感知）。
// 状态机/告警形态照 mt_health_monitor 同款（venue→lastState map，转换才写）。
// 边界：只提示不划转；venue 未配置/fetch 败=静默跳过不告警。
package dashboard

import (
	"context"
	"fmt"
	"log"
	"sort"
	"sync"
	"time"
)

// idleCapitalFloorUSD 闲置下限：低于此额的零头不提示（不够开一个有意义的仓位）。
const idleCapitalFloorUSD = 500.0

// idleCapitalRatio 剪刀差比：可转可用 > 立即可用×0.5 才算「钱躺着用不上」。
const idleCapitalRatio = 0.5

// idleCapitalVenueSet 监控域（子钱包面 venue；MT 无可转钱包天然不在域）。
var idleCapitalVenueSet = map[string]bool{"okx": true, "gate": true}

// IdleCapitalIdle 判据纯函数（导出可测）：transferable>$500 且>available×0.5；
// available≤0 不算闲置（无可用读数时不该喊「钱闲着」——fail-safe 不误报）。
func IdleCapitalIdle(transferable, available float64) bool {
	return available > 0 && transferable > idleCapitalFloorUSD && transferable > available*idleCapitalRatio
}

// IdleCapitalMonitor 闲置资金监控（ticker+转换式告警状态机）。
type IdleCapitalMonitor struct {
	svc    *Service
	tick   time.Duration
	warned map[int64]bool // realOnlyConfigs warn-once 独立实例（不与 Service 共享）

	mu   sync.Mutex
	idle map[string]bool // venue→当前是否闲置态
}

// NewIdleCapitalMonitor 构造（svc nil 亦可——Run nil-safe 不起作用不报错）。
func NewIdleCapitalMonitor(svc *Service, tick time.Duration) *IdleCapitalMonitor {
	return &IdleCapitalMonitor{svc: svc, tick: tick, warned: map[int64]bool{}, idle: map[string]bool{}}
}

// Run 轮询直至 ctx 取消（boot 首拍即查；模式同 MTHealthMonitor.Run）。
func (m *IdleCapitalMonitor) Run(ctx context.Context) {
	if m == nil || m.svc == nil {
		return
	}
	m.poll(ctx)
	ticker := time.NewTicker(m.tick)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.poll(ctx)
		}
	}
}

// poll 一轮：venue 集（exchange_configs real 行 venue∈{okx,gate} 去重）×判据×状态机。
func (m *IdleCapitalMonitor) poll(ctx context.Context) {
	for _, v := range m.realVenues(ctx) {
		va := m.svc.venueAvailability(ctx, v)
		if va == nil {
			continue // 未配置/fetch 败→静默跳过不告警
		}
		var avail, transfer float64
		for _, n := range va.Nodes {
			if n == nil || n.Detail != "" {
				continue // 面败节点不进合计（少算=不误报）
			}
			avail += n.AvailableUSD
			transfer += n.TransferableUSD
		}
		m.transition(ctx, v, IdleCapitalIdle(transfer, avail), transfer, avail)
	}
	// D-652-C 搭车核验：capital marked 行桶值锚→fulfilled / 24h 悬空 warn
	// （同 30min 拍零新定时器——读败该支 fail-closed 不影响闲置判据主径）。
	m.verifyMarkedCapital(ctx)
}

// realVenues 监控 venue 集=real 配置行中 venue∈{okx,gate} 去重（sim 行 fail-closed 不入）。
func (m *IdleCapitalMonitor) realVenues(ctx context.Context) []string {
	db := m.svc.gradDB()
	if db == nil {
		return nil
	}
	cfgs, err := db.ListExchangeConfigs(ctx)
	if err != nil {
		log.Printf("[idle-capital] list exchange configs: %v", err)
		return nil
	}
	set := map[string]bool{}
	for _, c := range realOnlyConfigs(cfgs, &m.warned, "idle-capital") {
		if idleCapitalVenueSet[c.Venue] {
			set[c.Venue] = true
		}
	}
	out := make([]string, 0, len(set))
	for v := range set {
		out = append(out, v)
	}
	sort.Strings(out)
	return out
}

// transition 状态机：仅入/出闲置态转换时写一条 info 告警（防洪水同 mt-health）。
func (m *IdleCapitalMonitor) transition(ctx context.Context, venue string, idle bool, transfer, avail float64) {
	m.mu.Lock()
	was := m.idle[venue]
	m.idle[venue] = idle
	m.mu.Unlock()
	if idle == was {
		return
	}
	var msg string
	if idle {
		msg = fmt.Sprintf("闲置资金 %s：可转可用 $%.2f 闲置（立即可用 $%.2f）——划转可解锁更大头寸", venue, transfer, avail)
	} else {
		msg = fmt.Sprintf("闲置资金 %s：已恢复（可转可用 $%.2f / 立即可用 $%.2f）", venue, transfer, avail)
	}
	db := m.svc.gradDB()
	if db == nil {
		return
	}
	if err := db.InsertAlert(ctx, nil, "info", msg); err != nil {
		log.Printf("[idle-capital] insert alert: %v", err)
	}
}
