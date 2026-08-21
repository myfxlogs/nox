// Package position 实现持仓收敛监控（§13.4）。
package position

import (
	"context"
	"log"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/store/pgstore"
)

// Monitor 订阅 OPEN/LEGGED 持仓涉及的报价事件，实时重算平仓净收益。
// 达阈值 → "建议平仓"告警；LEGGED 持仓告警未 Ack 则冷却重发（D-026 语义修订：持续→冷却，D-072 铃铛治理）。
type Monitor struct {
	bus   *bus.QuoteBus
	store *pgstore.Store

	mu          sync.Mutex
	positions   map[int64]*pgstore.Position // 缓存 OPEN/LEGGED 持仓
	lastAlert   map[int64]time.Time         // position → 上次告警时间
	throttle    time.Duration               // 同 key 告警冷却（ARB_ALERT_RESEND_INTERVAL，默认 10m）
	closeBpsThr float64                     // 建议平仓阈值（净收益 bps 降至此值以下时告警）
}

// New 创建持仓监控器。resendInterval = 同持仓同类告警冷却（ARB_ALERT_RESEND_INTERVAL，默认 10m）。
func New(b *bus.QuoteBus, store *pgstore.Store, closeBpsThreshold float64, resendInterval time.Duration) *Monitor {
	if resendInterval <= 0 {
		resendInterval = 10 * time.Minute
	}
	return &Monitor{
		bus:         b,
		store:       store,
		positions:   make(map[int64]*pgstore.Position),
		lastAlert:   make(map[int64]time.Time),
		throttle:    resendInterval,
		closeBpsThr: closeBpsThreshold,
	}
}

// Run 启动监控。阻塞至 ctx 取消。
func (m *Monitor) Run(ctx context.Context) {
	// 启动时加载 OPEN/LEGGED 持仓
	if err := m.loadPositions(ctx); err != nil {
		log.Printf("[position-monitor] load: %v", err)
	}
	// 定期刷新持仓列表（新开仓的持仓需要被发现）
	go m.refreshLoop(ctx)

	ch, cancel := m.bus.Subscribe("")
	defer cancel()
	for {
		select {
		case <-ctx.Done():
			return
		case q, ok := <-ch:
			if !ok {
				return
			}
			m.onQuote(ctx, q)
		}
	}
}

func (m *Monitor) loadPositions(ctx context.Context) error {
	positions, err := m.store.ListPositions(ctx, "", 200, 0)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range positions {
		p := &positions[i]
		if p.Status == "OPEN" || p.Status == "LEGGED" {
			m.positions[p.ID] = p
		}
	}
	log.Printf("[position-monitor] loaded %d open/legged positions", len(m.positions))
	return nil
}

func (m *Monitor) refreshLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := m.loadPositions(ctx); err != nil {
				log.Printf("[position-monitor] refresh: %v", err)
			}
		}
	}
}

func (m *Monitor) onQuote(ctx context.Context, q bus.Quote) {
	m.mu.Lock()
	defer m.mu.Unlock()

	for _, p := range m.positions {
		if p.Status == "LEGGED" {
			// LEGGED 持仓：未确认告警冷却重发（D-026 语义修订：持续→冷却，D-072 铃铛治理）
			last := m.lastAlert[p.ID]
			if time.Since(last) >= m.throttle {
				m.alertLegged(ctx, p)
				m.lastAlert[p.ID] = time.Now()
			}
			continue
		}

		// 息差型（SWAPARB/CARRY/FUNDING_HEDGE/PERP_FUNDING）跳过收敛告警——
		// 长期持有型不该建议平仓（D-072 铃铛治理：#264/#266 每日 1226 条刷屏根因）。
		// 判定口径与 autotrade.checkExitCondition 同源（IsCarryType 共用 helper，P3 单一真相）。
		if IsCarryType(p.Type) {
			continue
		}

		// OPEN 收敛型持仓：重算当前平仓净收益
		// 简化：用 expected_net_bps 作为基准，当报价恶化导致净收益低于阈值时告警
		// 完整实现需要查 legs 并按当前报价重算
		if p.ExpectedNetBps <= m.closeBpsThr {
			last := m.lastAlert[p.ID]
			if time.Since(last) >= m.throttle {
				m.alertSuggestClose(ctx, p)
				m.lastAlert[p.ID] = time.Now()
			}
		}
	}
}

func (m *Monitor) alertLegged(ctx context.Context, p *pgstore.Position) {
	msg := formatLeggedAlert(p)
	if err := m.store.InsertAlert(ctx, nil, "critical", msg); err != nil {
		log.Printf("[position-monitor] alert legged %d: %v", p.ID, err)
	}
}

func (m *Monitor) alertSuggestClose(ctx context.Context, p *pgstore.Position) {
	msg := formatCloseAlert(p)
	if err := m.store.InsertAlert(ctx, nil, "warn", msg); err != nil {
		log.Printf("[position-monitor] alert close %d: %v", p.ID, err)
	}
}

func formatLeggedAlert(p *pgstore.Position) string {
	return "持仓 #" + int64ToStr(p.ID) + " LEGGED：单腿暴露，需手动处置"
}

func formatCloseAlert(p *pgstore.Position) string {
	return "持仓 #" + int64ToStr(p.ID) + " 净收益降至 " + float64ToStr(p.ExpectedNetBps) + " bps，建议平仓"
}

func int64ToStr(n int64) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// float64ToStr 浮点→字符串（2 位小数）。D-072 铃铛治理顺修：负数分支小数点 bug（-1.23 → "-1.-23"）。
// 修复：取绝对值后分别格式化整数/小数部分，负号前置。
func float64ToStr(f float64) string {
	neg := f < 0
	abs := f
	if neg {
		abs = -f
	}
	intPart := int64(abs)
	frac := int64((abs - float64(intPart)) * 100)
	s := int64ToStr(intPart) + "." + int64ToStr(frac)
	if neg {
		s = "-" + s
	}
	return s
}
