// nav.go —— D-264 M4.4①②③：NAV 单函数 + fin_nav_daily 日切 + fin_strategy_budget 读面。
// NAV 口径单源：Σ fin_venue_account_snapshot 最新池 equity（USD-only 过滤）。
// 写口过滤已保 real-only（scheduler/account_event 写时只收 real，D-006 径）；
// 本函数不重复筛 is_real——快照表即账本真值。
package pgstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// NavReport NAV 口径单源结果（三消费面=deptreport/投资人页 IR/配置器 共读）。
type NavReport struct {
	NavUSD        float64 // Σ 池权益（USD 计价；非 USD 池跳过不计）
	IdleUSD       float64 // Σ 池可动余额（同口径）
	Pools         []ExplorerIdlePool
	SkippedNonUSD int // 非 USD 池跳过计数（诚实口径不假换算）
	ComputedAt    time.Time
}

// SumNav 池集→NAV 聚合规则唯一实现（ComputeNav/测试 fake 同径复用）。
// USD-only：非 USD 池跳过+计数（诚实口径不假换算）；缺快照 venue 自然缺席。
func SumNav(pools []ExplorerIdlePool, now time.Time) *NavReport {
	r := &NavReport{Pools: pools, ComputedAt: now}
	for _, p := range pools {
		if p.Currency != "USD" {
			r.SkippedNonUSD++
			continue
		}
		r.NavUSD += p.Equity
		r.IdleUSD += p.Balance
	}
	return r
}

// ComputeNav NAV 单函数（M4.4②）：包内共享 ExplorerIdleFunds 池查询，
// 聚合规则唯一——USD-only + Σequity/Σbalance；缺快照 venue 自然缺席（诚实零值不编造）。
func (s *Store) ComputeNav(ctx context.Context, now time.Time) (*NavReport, error) {
	pools, err := s.ExplorerIdleFunds(ctx, now)
	if err != nil {
		return nil, err
	}
	return SumNav(pools, now), nil
}

// NavDailyRow fin_nav_daily 行（日切幂等 upsert——同日复算覆盖最新值）。
type NavDailyRow struct {
	DayStart   time.Time       `json:"-"`
	NavUSD     float64         `json:"nav_usd"`
	IdleUSD    float64         `json:"idle_usd"`
	Venues     json.RawMessage `json:"-"`
	ComputedAt time.Time       `json:"-"`
}

// UpsertNavDaily 日切落行（PK 冲突覆盖——幂等；venues=池分解快照 jsonb）。
func (s *Store) UpsertNavDaily(ctx context.Context, r NavDailyRow) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_nav_daily (day_start, nav_usd, idle_usd, venues, computed_at)
		VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (day_start) DO UPDATE SET
			nav_usd=$2, idle_usd=$3, venues=$4, computed_at=$5`,
		r.DayStart, r.NavUSD, r.IdleUSD, r.Venues, r.ComputedAt)
	if err != nil {
		return fmt.Errorf("upsert nav_daily: %w", err)
	}
	return nil
}

// LatestNavDaily 最新日切行（看板/复盘读面；缺行 ok=false 非错误，读败 fail-closed）。
func (s *Store) LatestNavDaily(ctx context.Context) (*NavDailyRow, bool, error) {
	var r NavDailyRow
	err := s.pool.QueryRow(ctx, `
		SELECT day_start, nav_usd, idle_usd, venues, computed_at
		FROM fin_nav_daily ORDER BY day_start DESC LIMIT 1`).
		Scan(&r.DayStart, &r.NavUSD, &r.IdleUSD, &r.Venues, &r.ComputedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &r, true, nil
}

// StrategyBudget fin_strategy_budget 行（配置器写，闸层只读）。
type StrategyBudget struct {
	Strategy    string          `json:"strategy"`
	BudgetUSD   float64         `json:"budget_usd"`
	DerivedFrom json.RawMessage `json:"derived_from"`
	UpdatedAt   time.Time       `json:"updated_at"`
}

// GetStrategyBudget 单策略预算（缺席 ok=false——BudgetChecker fail-closed 锚；读败 fail-closed）。
func (s *Store) GetStrategyBudget(ctx context.Context, strategy string) (*StrategyBudget, bool, error) {
	var b StrategyBudget
	err := s.pool.QueryRow(ctx, `
		SELECT strategy, budget_usd, derived_from, updated_at
		FROM fin_strategy_budget WHERE strategy=$1`, strategy).
		Scan(&b.Strategy, &b.BudgetUSD, &b.DerivedFrom, &b.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return &b, true, nil
}

// UpsertStrategyBudget 配置器独写口（行存在覆盖——日切重算幂等）。
func (s *Store) UpsertStrategyBudget(ctx context.Context, b StrategyBudget) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_strategy_budget (strategy, budget_usd, derived_from, updated_at)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (strategy) DO UPDATE SET
			budget_usd=$2, derived_from=$3, updated_at=$4`,
		b.Strategy, b.BudgetUSD, b.DerivedFrom, b.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert strategy_budget: %w", err)
	}
	return nil
}

// ListStrategyBudgets 全部预算行（配置器全量重算/看板读面）。
func (s *Store) ListStrategyBudgets(ctx context.Context) ([]StrategyBudget, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT strategy, budget_usd, derived_from, updated_at
		FROM fin_strategy_budget ORDER BY strategy`)
	if err != nil {
		return nil, fmt.Errorf("list strategy_budget: %w", err)
	}
	defer rows.Close()
	var out []StrategyBudget
	for rows.Next() {
		var b StrategyBudget
		if err := rows.Scan(&b.Strategy, &b.BudgetUSD, &b.DerivedFrom, &b.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan strategy_budget: %w", err)
		}
		out = append(out, b)
	}
	return out, rows.Err()
}
