// Package watch 业主盯盘引擎（业主令 10-14，业主视窗两件套 B）：
// watch_rule 行 → QuoteBus 通配订阅（symbol="" 收全源 tick）事件驱动判条件——
// 禁另起轮询 ticker 读报价（D-004：流来即判）；规则集由 configKick 触发重载
// （D-469b 同径，config 类 mutation 即到即生效）。
//
// fail-closed：kind≠'price' 的规则拒评不静默跳（chk 已拦入册，双保险）；
// tick 陈旧（now−q.Time>staleTTL）不触发——报价冻结期不许按最后值判。
// once 触发自翻 disabled 留痕；repeat 走 cooldown_sec 防抖。
package watch

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"nox/internal/data/bus"
	"nox/internal/store/pgstore"
)

// Store 引擎所需存储面（pgstore.Store 子集，测试可 fake）。
type Store interface {
	ListWatchRules(ctx context.Context) ([]pgstore.WatchRule, error)
	MarkWatchFired(ctx context.Context, id int64, disable bool) error
	InsertAlert(ctx context.Context, posID *int64, level, message string) error
}

// Engine 盯盘评估器。
type Engine struct {
	st       Store
	qbus     *bus.QuoteBus
	staleTTL time.Duration
	now      func() time.Time // 测试可注入

	mu    sync.RWMutex
	rules map[int64]pgstore.WatchRule // enabled 全集（id 索引）
}

// New 装配（staleTTL=tick 新鲜判窗——生产装配取与报价存活窗同量级）。
func New(st Store, qbus *bus.QuoteBus, staleTTL time.Duration) *Engine {
	return &Engine{st: st, qbus: qbus, staleTTL: staleTTL, now: time.Now}
}

// Reload 重载规则集（boot + configKick 触发；读败留旧集不空窗，log 留痕）。
func (e *Engine) Reload(ctx context.Context) {
	rules, err := e.st.ListWatchRules(ctx)
	if err != nil {
		log.Printf("[watch] reload rules: %v（留旧集）", err)
		return
	}
	m := make(map[int64]pgstore.WatchRule, len(rules))
	for _, r := range rules {
		if r.Enabled {
			m[r.ID] = r
		}
	}
	e.mu.Lock()
	e.rules = m
	e.mu.Unlock()
}

// Run 主循环：通配订阅 qbus + kick 通道（kickCh nil 容忍=boot 一次集）。
func (e *Engine) Run(ctx context.Context, kickCh <-chan struct{}) {
	e.Reload(ctx)
	ch, cancel := e.qbus.Subscribe("")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-kickCh:
			if !ok {
				kickCh = nil
				continue
			}
			e.Reload(ctx)
		case q, ok := <-ch:
			if !ok {
				return // bus 关闭=退出
			}
			e.onQuote(ctx, q)
		}
	}
}

// onQuote tick 即判：subject 匹配+venue_key 过滤（空=全源）→ stale 守卫 → 阈值判 → 触发落账。
func (e *Engine) onQuote(ctx context.Context, q bus.Quote) {
	if q.Time.IsZero() || e.now().Sub(q.Time) > e.staleTTL {
		return // 陈旧 tick 不触发不伪造
	}
	mid := q.Mid()
	e.mu.RLock()
	rules := e.rules
	e.mu.RUnlock()
	vk := q.VenueKey()
	for _, r := range rules {
		if r.Kind != "price" {
			log.Printf("[watch] rule#%d kind=%q 不支持（fail-closed 报缺）", r.ID, r.Kind)
			continue
		}
		if r.Subject != q.Symbol || (r.VenueKey != "" && r.VenueKey != vk) {
			continue
		}
		if !hit(r.Op, mid, r.Threshold) {
			continue
		}
		if r.FireMode == "repeat" && r.LastFiredAt != nil &&
			e.now().Sub(*r.LastFiredAt) < time.Duration(r.CooldownSec)*time.Second {
			continue // cooldown 防抖
		}
		e.fire(ctx, r, q, mid)
	}
}

// fire 触发：InsertAlert warn（穿既有飞书滤噪闸）+MarkWatchFired（once 自翻 disabled）；
// 内存集同步摘除——防 reload 前同 tick 二次触发。
func (e *Engine) fire(ctx context.Context, r pgstore.WatchRule, q bus.Quote, mid float64) {
	disable := r.FireMode != "repeat"
	if err := e.st.InsertAlert(ctx, nil, "warn", fmt.Sprintf(
		"盯盘触发#%d: %s %.6g %s %.6g @%s（%s）", r.ID, q.Symbol, mid, opText(r.Op), r.Threshold, vkOrAll(q, r), r.Note)); err != nil {
		log.Printf("[watch] rule#%d alert: %v", r.ID, err)
	}
	if err := e.st.MarkWatchFired(ctx, r.ID, disable); err != nil {
		log.Printf("[watch] rule#%d mark fired: %v", r.ID, err)
		return // 落账败=不摘内存（下次重判），宁可重告警不静默丢触发
	}
	e.mu.Lock()
	if disable {
		delete(e.rules, r.ID)
	} else {
		t := e.now()
		r.LastFiredAt = &t
		e.rules[r.ID] = r
	}
	e.mu.Unlock()
}

// hit 阈值判（mid 价基准——bid/ask 中点，与持仓评估同口径）。
func hit(op string, mid, thr float64) bool {
	switch op {
	case "lt":
		return mid < thr
	case "le":
		return mid <= thr
	case "gt":
		return mid > thr
	case "ge":
		return mid >= thr
	}
	return false
}

func opText(op string) string {
	switch op {
	case "lt":
		return "<"
	case "le":
		return "<="
	case "gt":
		return ">"
	case "ge":
		return ">="
	}
	return op
}

func vkOrAll(q bus.Quote, r pgstore.WatchRule) string {
	if r.VenueKey == "" {
		return "全源:" + q.VenueKey()
	}
	return r.VenueKey
}
