// Package budget —— D-264 M4.4③④：资金配置器（fin_strategy_budget 独写域）+
// BudgetChecker 闸侧只读实现。
//
// 推导规则（确定性零 LLM）：
//
//	deployable = nav × (1 − reserve_pct/100)（reserve_pct 约束缺席=0 储备）
//	每 live 策略 budget = min(maxNotional, deployable / liveCount)
//
// live 集=fin_strategy.state='live'（M4.2 状态机）；零 live=不写行。
// derived_from 留推导输入痕——审计可复算。
package budget

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"time"

	"nox/internal/risk/gate"
	"nox/internal/store/pgstore"
)

// Store 配置器读写真值面（*pgstore.Store 原生满足）。
type Store interface {
	ComputeNav(ctx context.Context, now time.Time) (*pgstore.NavReport, error)
	UpsertNavDaily(ctx context.Context, r pgstore.NavDailyRow) error
	ListStrategiesByState(ctx context.Context, state string) ([]string, error)
	ListCapabilityConstraints(ctx context.Context) ([]pgstore.CapabilityConstraint, error)
	UpsertStrategyBudget(ctx context.Context, b pgstore.StrategyBudget) error
}

// ReservePctOf reserve_pct 约束读（缺约束=0 储备——deployable=全 NAV；读败 fail-closed 由调用侧拦）。
func ReservePctOf(cons []pgstore.CapabilityConstraint) float64 {
	for _, c := range cons {
		if c.Subject == "reserve_pct" && c.ValueNum != nil {
			return *c.ValueNum
		}
	}
	return 0
}

// derive 纯函数：每 live 策略预算额（零 live → 空 map）。
// D-522③：按结算币种分组 derive——策略预算只吃同币种 NAV 桶（USD 策略吃 USD 桶、
// USDT 策略吃 USDT 桶，跨币永不加总 D-285）；结算币种未知=fail-closed 跳过不产预算。
func derive(navByCcy map[string]pgstore.NavBucket, reservePct, maxNotional float64, live []string, ccyOf func(string) (string, bool)) map[string]float64 {
	out := make(map[string]float64, len(live))
	byCcy := map[string][]string{}
	for _, s := range live {
		ccy, ok := ccyOf(s)
		if !ok {
			// 注册面未载 → USD 桶（历史单桶语义保留——CFD/MT 轨结算户皆 USD；
			// 显式 fail-closed 只留给「桶缺席」而非「名未注册」，不致存量策略断供）。
			log.Printf("[budget] %s 结算币种未注册——回退 USD 桶", s)
			ccy = "USD"
		}
		byCcy[ccy] = append(byCcy[ccy], s)
	}
	for ccy, group := range byCcy {
		bucket := navByCcy[ccy] // 缺席桶=零值→deployable=0 预算 0（fail-closed 不放无本预算）
		deployable := bucket.Nav * (1 - reservePct/100)
		if deployable < 0 {
			deployable = 0
		}
		share := deployable / float64(len(group))
		if share > maxNotional {
			share = maxNotional
		}
		for _, s := range group {
			out[s] = share
		}
	}
	return out
}

// DailyCut 日切一班（并入 deptreport 07:05 BJ 拍——零新定时器）：
// ① fin_nav_daily 幂等落行；② live 策略预算重算落 fin_strategy_budget。
// nav 计算失败=整班失败返回 err（日切 fail-closed，半落不留态——upsert 幂等可重跑）。
// maxNotional=cfg.AutoTradeMaxNotional（与 gate 单笔上限同源）。
func DailyCut(ctx context.Context, s Store, now, dayStart time.Time, maxNotional float64) error {
	nav, err := s.ComputeNav(ctx, now)
	if err != nil {
		return fmt.Errorf("nav 计算败（日切拒落）: %w", err)
	}
	// D-522①：fin_nav_daily 按结算币种分行（PK=day_start×ccy；venues 按币种分组）。
	ccys := make([]string, 0, len(nav.ByCcy))
	for c := range nav.ByCcy {
		ccys = append(ccys, c)
	}
	sort.Strings(ccys)
	for _, ccy := range ccys {
		bucket := nav.ByCcy[ccy]
		var ccyPools []pgstore.ExplorerIdlePool
		for _, p := range nav.Pools {
			if p.Currency == ccy && !p.Ghost {
				ccyPools = append(ccyPools, p)
			}
		}
		venues, _ := json.Marshal(ccyPools)
		if err := s.UpsertNavDaily(ctx, pgstore.NavDailyRow{
			DayStart: dayStart, Currency: ccy, NavUSD: bucket.Nav, IdleUSD: bucket.Idle,
			Venues: venues, ComputedAt: nav.ComputedAt,
		}); err != nil {
			return err
		}
	}
	live, err := s.ListStrategiesByState(ctx, "live")
	if err != nil {
		return fmt.Errorf("live 策略集读败: %w", err)
	}
	cons, err := s.ListCapabilityConstraints(ctx)
	if err != nil {
		return fmt.Errorf("约束读败: %w", err)
	}
	reservePct := ReservePctOf(cons)
	// D-522③：derive 键=策略结算币种（gate.StrategySettleCcy 注册面；未知币种 fail-closed）。
	settleOf := map[string]string{}
	allocs := derive(nav.ByCcy, reservePct, maxNotional, live, gate.StrategySettleCcy)
	for _, st := range live {
		if c, ok := gate.StrategySettleCcy(st); ok {
			settleOf[st] = c
		}
	}
	now2 := time.Now()
	for strategy, usd := range allocs {
		ccy := settleOf[strategy]
		var groupN int
		for _, s2 := range live {
			if settleOf[s2] == ccy {
				groupN++
			}
		}
		from, _ := json.Marshal(map[string]any{
			"ccy": ccy, "nav_ccy": nav.ByCcy[ccy].Nav, "reserve_pct": reservePct,
			"live_count_ccy": groupN,
			"max_notional":   maxNotional, "rule": "min(maxNotional, nav_ccy*(1-reserve)/liveCountCcy)",
		})
		if err := s.UpsertStrategyBudget(ctx, pgstore.StrategyBudget{
			Strategy: strategy, BudgetUSD: usd, DerivedFrom: from, UpdatedAt: now2,
		}); err != nil {
			return err
		}
	}
	log.Printf("[budget] 日切 ccy=%d 桶 live=%d 预算落 %d 行", len(ccys), len(live), len(allocs))
	return nil
}

// Checker gate.BudgetChecker 实现（闸层只读 fin_strategy_budget）。
type Checker struct {
	s BudgetStore
}

// BudgetStore 闸侧最小读口（*pgstore.Store 满足；测试注入 fake）。
type BudgetStore interface {
	GetStrategyBudget(ctx context.Context, strategy string) (*pgstore.StrategyBudget, bool, error)
}

// NewChecker 装配闸侧预算检（cmd/core setup 挂 RiskGate——配置器未产行=全单拒安全侧）。
func NewChecker(s BudgetStore) *Checker { return &Checker{s: s} }

// CheckBudget 预算一致性检（M4.3 接口契约）：行缺席/读败/超额=fail-closed 拒。
func (c *Checker) CheckBudget(ctx context.Context, strategy string, notionalUSD float64) (bool, string) {
	b, ok, err := c.s.GetStrategyBudget(ctx, strategy)
	if err != nil {
		return false, fmt.Sprintf("预算读败（拒单）: %v", err)
	}
	if !ok {
		return false, fmt.Sprintf("策略 %s 无预算行（拒单——配置器未配置）", strategy)
	}
	if notionalUSD > b.BudgetUSD {
		return false, fmt.Sprintf("策略 %s 名义 $%.0f > 预算 $%.0f（拒单）", strategy, notionalUSD, b.BudgetUSD)
	}
	return true, ""
}
