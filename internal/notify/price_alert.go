// D-282 价格提醒规则引擎：订阅 QuoteBus，按 price_alerts 规则边缘触发告警。
// 独立 goroutine + QuoteBus.Subscribe，不接入 collect.Scheduler。
// 飞书提醒通过 InsertAlert(level, msg) 走 D-264 alertHook 链路。
package notify

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/safego"
)

// PriceAlertRule 是提醒规则（notify 包本地类型，避免 notify→pgstore 依赖）。
type PriceAlertRule struct {
	ID          int64
	Symbol      string
	Op          string // ">=" 或 "<="
	Threshold   float64
	Level       string // info/warn/critical
	Message     string
	LastAlertAt *time.Time
}

// PriceAlertStore 提醒服务存储接口（pgstore 实现，避免 notify→pgstore 依赖）。
type PriceAlertStore interface {
	ListEnabledPriceAlerts(ctx context.Context) ([]PriceAlertRule, error)
	UpdatePriceAlertLastAlert(ctx context.Context, id int64, at time.Time) error
}

// PriceAlertService 价格提醒规则引擎。
type PriceAlertService struct {
	kickCh   <-chan struct{} // D-469b S5：nil 通道 select 阻塞安全=纯 ticker
	bus      *bus.QuoteBus
	store    PriceAlertStore
	alertFn  AlertFn       // 复用 repo_reminder.go 的 AlertFn 类型（func(level, msg string)）
	throttle time.Duration // 同规则节流间隔（默认 30 分钟）

	mu        sync.Mutex
	rules     map[int64]*PriceAlertRule // 缓存 enabled 规则
	level     map[int64]bool            // id → 当前是否处于触发态（边缘触发）
	lastAlert map[int64]time.Time       // id → 上次告警时间（节流兜底）

	now func() time.Time
}

// NewPriceAlertService 创建价格提醒服务。
// throttle=同规则两次告警最小间隔（默认 30 分钟）。
func NewPriceAlertService(bus *bus.QuoteBus, store PriceAlertStore, alertFn AlertFn, throttle time.Duration) *PriceAlertService {
	if throttle <= 0 {
		throttle = 30 * time.Minute
	}
	return &PriceAlertService{
		bus:       bus,
		store:     store,
		alertFn:   alertFn,
		throttle:  throttle,
		rules:     make(map[int64]*PriceAlertRule),
		level:     make(map[int64]bool),
		lastAlert: make(map[int64]time.Time),
		now:       time.Now,
	}
}

// SetKickChan D-469b S5：注入规则变更 kick 通道（装配=configKick 订阅端）。
func (s *PriceAlertService) SetKickChan(ch <-chan struct{}) { s.kickCh = ch }

// SetNow 替换当前时间函数（测试注入）。
func (s *PriceAlertService) SetNow(fn func() time.Time) { s.now = fn }

// Kick D-469b S5：规则 mutation RPC 成功路径 kick 通道（nil=纯 ticker 兜底）。
// chan 而非 topic——PriceAlertService 无 bus 依赖先例，订阅解耦在装配侧。
var _ = struct{}{} // 保持注释块独立
// 字段声明于 struct（见 PriceAlertService.Kick）。

// Run 启动监控。阻塞至 ctx 取消。
// 订阅所有 symbol 的报价（"" = 全量订阅），按规则 symbol 过滤。
func (s *PriceAlertService) Run(ctx context.Context) {
	if err := s.loadRules(ctx); err != nil {
		log.Printf("[price_alert] load: %v", err)
	}
	safego.Go("price_alert:refreshLoop", func() { s.refreshLoop(ctx) })
	ch, cancel := s.bus.Subscribe("")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-ch:
			if !ok {
				return
			}
			s.onQuote(ctx, q)
		}
	}
}

// loadRules 加载 enabled 规则到缓存。
func (s *PriceAlertService) loadRules(ctx context.Context) error {
	rules, err := s.store.ListEnabledPriceAlerts(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	seen := make(map[int64]bool, len(rules))
	for i := range rules {
		r := &rules[i]
		seen[r.ID] = true
		s.rules[r.ID] = r
		if r.LastAlertAt != nil {
			s.lastAlert[r.ID] = *r.LastAlertAt
		}
	}
	for id := range s.rules {
		if !seen[id] {
			delete(s.rules, id)
			delete(s.level, id)
			delete(s.lastAlert, id)
		}
	}
	log.Printf("[price_alert] loaded %d enabled rules", len(s.rules))
	return nil
}

func (s *PriceAlertService) refreshLoop(ctx context.Context) {
	// D-469b S5：mutation RPC kick 主路+5min 兜底（带外 SQL 改行）。
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kickCh:
			if err := s.loadRules(ctx); err != nil {
				log.Printf("[price_alert] refresh: %v", err)
			}
		case <-ticker.C:
			if err := s.loadRules(ctx); err != nil {
				log.Printf("[price_alert] refresh: %v", err)
			}
		}
	}
}

// onQuote 按 symbol 过滤规则，边缘触发 + 节流。
func (s *PriceAlertService) onQuote(ctx context.Context, q bus.Quote) {
	mid := q.Mid()
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rules {
		if r.Symbol != q.Symbol {
			continue
		}
		triggered := ruleMatch(r, mid)
		prev := s.level[r.ID] // 首次=false（未触发态基线）
		// 边缘触发：仅 false→true（未触发→触发）时告警
		if triggered && !prev && s.now().Sub(s.lastAlert[r.ID]) >= s.throttle {
			s.alert(ctx, r, mid)
			s.lastAlert[r.ID] = s.now()
			if err := s.store.UpdatePriceAlertLastAlert(ctx, r.ID, s.now()); err != nil {
				log.Printf("[price_alert] update last_alert %d: %v", r.ID, err)
			}
		}
		s.level[r.ID] = triggered
	}
}

// ruleMatch 判断规则是否命中。
func ruleMatch(r *PriceAlertRule, mid float64) bool {
	switch r.Op {
	case ">=":
		return mid >= r.Threshold
	case "<=":
		return mid <= r.Threshold
	default:
		return false
	}
}

// alert 发送告警（InsertAlert → alertHook → 飞书）。
func (s *PriceAlertService) alert(ctx context.Context, r *PriceAlertRule, mid float64) {
	if s.alertFn == nil {
		return
	}
	msg := fmt.Sprintf("[价格提醒] %s %s %.6f（当前 %.6f）\n%s", r.Symbol, r.Op, r.Threshold, mid, r.Message)
	s.alertFn(r.Level, msg)
}
