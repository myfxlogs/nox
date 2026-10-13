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
	"time"

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
func derive(nav, reservePct, maxNotional float64, live []string) map[string]float64 {
	out := make(map[string]float64, len(live))
	if len(live) == 0 || nav <= 0 {
		return out
	}
	deployable := nav * (1 - reservePct/100)
	if deployable < 0 {
		deployable = 0
	}
	share := deployable / float64(len(live))
	if share > maxNotional {
		share = maxNotional
	}
	for _, s := range live {
		out[s] = share
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
	venues, _ := json.Marshal(nav.Pools)
	if err := s.UpsertNavDaily(ctx, pgstore.NavDailyRow{
		DayStart: dayStart, NavUSD: nav.NavUSD, IdleUSD: nav.IdleUSD,
		Venues: venues, ComputedAt: nav.ComputedAt,
	}); err != nil {
		return err
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
	allocs := derive(nav.NavUSD, reservePct, maxNotional, live)
	now2 := time.Now()
	for strategy, usd := range allocs {
		from, _ := json.Marshal(map[string]any{
			"nav_usd": nav.NavUSD, "reserve_pct": reservePct, "live_count": len(live),
			"max_notional": maxNotional, "rule": "min(maxNotional, nav*(1-reserve)/liveCount)",
		})
		if err := s.UpsertStrategyBudget(ctx, pgstore.StrategyBudget{
			Strategy: strategy, BudgetUSD: usd, DerivedFrom: from, UpdatedAt: now2,
		}); err != nil {
			return err
		}
	}
	log.Printf("[budget] 日切 nav=%.0f idle=%.0f live=%d 预算落 %d 行", nav.NavUSD, nav.IdleUSD, len(live), len(allocs))
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
