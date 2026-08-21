package autotrade

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"arb/internal/bus"
	"arb/internal/dashboard"
	"arb/internal/store/pgstore"
)

// Config 自动交易引擎配置（D-070）。
type Config struct {
	Enabled      bool
	MaxNotional  float64
	MaxPositions int
	MaxDailyLoss float64
	MinNetBps    float64
	CloseBpsThr  float64       // 收敛型平仓阈值（expected_net_bps <= 此值则平仓）
	PollInterval time.Duration // OPEN 持仓扫描间隔
	NotionalUSD  float64       // 单笔名义额（用于算 qty）
}

// Engine 自动交易引擎：订阅机会 → 风控 → 执行 → 自动平仓 → 策略淘汰（D-070）。
type Engine struct {
	cfg      Config
	store    *pgstore.Store
	service  *dashboard.Service
	oppTopic *bus.Topic[pgstore.Opportunity]

	mu       sync.Mutex
	executed map[string]bool // 会话内已执行的机会 ID（防短时间内重复）
}

// New 创建自动交易引擎。
func New(cfg Config, store *pgstore.Store, service *dashboard.Service, oppTopic *bus.Topic[pgstore.Opportunity]) *Engine {
	return &Engine{
		cfg:      cfg,
		store:    store,
		service:  service,
		oppTopic: oppTopic,
		executed: make(map[string]bool),
	}
}

// Run 启动自动交易引擎（阻塞，ctx 取消时退出）。
func (e *Engine) Run(ctx context.Context) {
	if !e.cfg.Enabled {
		log.Println("[autotrade] disabled (ARB_AUTOTRADE_ENABLED=false)")
		return
	}
	log.Printf("[autotrade] enabled: maxNotional=$%.0f maxPos=%d maxDailyLoss=$%.0f minNetBps=%.1f poll=%s",
		e.cfg.MaxNotional, e.cfg.MaxPositions, e.cfg.MaxDailyLoss, e.cfg.MinNetBps, e.cfg.PollInterval)

	// 1. 机会订阅循环
	go e.oppLoop(ctx)

	// 2. 持仓扫描循环（自动平仓）
	e.positionLoop(ctx)
}

// oppLoop 订阅机会事件 → 风控 → 执行。
func (e *Engine) oppLoop(ctx context.Context) {
	ch, unsub := e.oppTopic.Subscribe()
	defer unsub()
	for {
		select {
		case <-ctx.Done():
			return
		case opp, ok := <-ch:
			if !ok {
				return
			}
			e.handleOpportunity(ctx, opp)
		}
	}
}

// handleOpportunity 处理一个机会事件：风控检查 → 执行 → 记录。
func (e *Engine) handleOpportunity(ctx context.Context, opp pgstore.Opportunity) {
	// 门槛：净收益
	if opp.NetBps < e.cfg.MinNetBps {
		return
	}
	// 会话内防重
	e.mu.Lock()
	if e.executed[opp.ID] {
		e.mu.Unlock()
		return
	}
	e.mu.Unlock()

	// 风控1：策略是否被淘汰
	disabled, err := e.store.IsStrategyDisabled(ctx, opp.Type)
	if err != nil {
		log.Printf("[autotrade] check strategy disabled: %v", err)
	} else if disabled {
		return
	}

	// 风控2：持仓数上限
	openCount, err := e.store.CountOpenPositions(ctx)
	if err != nil {
		log.Printf("[autotrade] count open positions: %v", err)
		return
	}
	if openCount >= e.cfg.MaxPositions {
		e.logTrade(ctx, &opp, 0, 0, "REJECTED", fmt.Sprintf("max positions reached (%d)", openCount))
		return
	}

	// 风控3：当日亏损上限
	dailyPnl, err := e.store.DailyRealizedPnl(ctx)
	if err != nil {
		log.Printf("[autotrade] daily pnl: %v", err)
		return
	}
	if dailyPnl <= -e.cfg.MaxDailyLoss {
		e.logTrade(ctx, &opp, 0, 0, "REJECTED", fmt.Sprintf("daily loss limit hit ($%.2f)", dailyPnl))
		return
	}

	// 风控4：幂等防重（DB 级）
	has, err := e.store.HasOpenPositionByOpportunity(ctx, opp.ID)
	if err != nil {
		log.Printf("[autotrade] check duplicate: %v", err)
		return
	}
	if has {
		return
	}

	// 计算 qty：按单笔名义额 / 买腿参考价
	qty := e.cfg.NotionalUSD / opp.PriceBuy
	if qty <= 0 {
		return
	}

	// 执行
	reqID := fmt.Sprintf("autotrade-%d-%s", time.Now().UnixMilli(), opp.ID)
	status, msg, posID := e.service.ExecuteOpportunityInternal(ctx, opp.ID, reqID, qty)

	// 记录
	e.mu.Lock()
	e.executed[opp.ID] = true
	e.mu.Unlock()

	if status == "FAILED" {
		if msg == "" {
			msg = "execute failed (pipeline returned FAILED)"
		}
		e.logTrade(ctx, &opp, 0, qty, "FAILED", msg)
		log.Printf("[autotrade] FAILED opp=%s type=%s qty=%.4f: %s", opp.ID, opp.Type, qty, msg)
	} else {
		e.logTrade(ctx, &opp, posID, qty, "EXECUTED", "")
		log.Printf("[autotrade] EXECUTED opp=%s type=%s qty=%.4f posID=%d status=%s", opp.ID, opp.Type, qty, posID, status)
	}
}

// positionLoop 定期扫描 OPEN 持仓 → 收敛型自动平仓。
func (e *Engine) positionLoop(ctx context.Context) {
	ticker := time.NewTicker(e.cfg.PollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.scanAndClose(ctx)
		}
	}
}

// scanAndClose 扫描 OPEN 持仓，收敛型达标则自动平仓。
func (e *Engine) scanAndClose(ctx context.Context) {
	positions, err := e.store.ListPositions(ctx, "OPEN", 50, 0)
	if err != nil {
		log.Printf("[autotrade] list open positions: %v", err)
		return
	}
	for _, p := range positions {
		// 息差型不主动平仓（持有赚 funding/swap）
		if p.Type == "FUNDING_HEDGE" || p.Type == "CARRY" {
			continue
		}
		// 收敛型：expected_net_bps <= closeBpsThr 则平仓
		if p.ExpectedNetBps <= e.cfg.CloseBpsThr {
			log.Printf("[autotrade] auto-close pos=%d type=%s netBps=%.2f <= %.1f", p.ID, p.Type, p.ExpectedNetBps, e.cfg.CloseBpsThr)
			if err := e.service.AutoClosePosition(ctx, p.ID); err != nil {
				log.Printf("[autotrade] auto-close pos=%d failed: %v", p.ID, err)
				continue
			}
			// 平仓后更新策略统计
			e.updateStrategyStat(ctx, &p)
		}
	}
}

// updateStrategyStat 平仓后更新策略统计 + 检查淘汰。
func (e *Engine) updateStrategyStat(ctx context.Context, p *pgstore.Position) {
	if p.RealizedPnl == nil {
		return
	}
	pnl := *p.RealizedPnl
	if err := e.store.UpdateStrategyStat(ctx, p.Type, pnl); err != nil {
		log.Printf("[autotrade] update strategy stat type=%s: %v", p.Type, err)
		return
	}
	// 检查淘汰规则：累计≥5笔 且 (胜率<30% 或 avg_pnl<0)
	stat, err := e.store.GetStrategyStat(ctx, p.Type)
	if err != nil {
		log.Printf("[autotrade] get strategy stat: %v", err)
		return
	}
	if stat.TotalTrades >= 5 && !stat.Disabled {
		if stat.WinRate < 0.3 || stat.AvgPnl < 0 {
			reason := fmt.Sprintf("auto-disabled: %d trades, winRate=%.0f%%, avgPnl=$%.2f", stat.TotalTrades, stat.WinRate*100, stat.AvgPnl)
			if err := e.store.DisableStrategy(ctx, p.Type, reason); err != nil {
				log.Printf("[autotrade] disable strategy: %v", err)
			} else {
				log.Printf("[autotrade] STRATEGY DISABLED type=%s: %s", p.Type, reason)
			}
		}
	}
}

// logTrade 记录交易日志到 DB。
func (e *Engine) logTrade(ctx context.Context, opp *pgstore.Opportunity, posID int64, qty float64, status, reason string) {
	var posIDPtr *int64
	if posID > 0 {
		posIDPtr = &posID
	}
	err := e.store.InsertAutotradeLog(ctx, &pgstore.AutotradeLog{
		OpportunityID: opp.ID,
		PositionID:    posIDPtr,
		Strategy:      opp.Type,
		Canonical:     opp.Canonical,
		VenueBuy:      opp.VenueBuy,
		VenueSell:     opp.VenueSell,
		Qty:           qty,
		Status:        status,
		Reason:        reason,
		NetBps:        opp.NetBps,
	})
	if err != nil {
		log.Printf("[autotrade] insert log: %v", err)
	}
}
