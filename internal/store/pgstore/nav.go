// nav.go —— D-264 M4.4①②③：NAV 单函数 + fin_nav_daily 日切 + fin_strategy_budget 读面。
// D-522：NAV 按结算币种分账——ByCcy map[ccy]分桶（USD/USDT 各行；跨币永不加总 D-285）；
// NavUSD/IdleUSD 保留=USD 桶别名（旧消费面兼容）。写口过滤已保 real-only（D-006 径）；
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

// NavBucket 单币种分桶（nav=Σequity，idle=Σbalance；D-522）。
type NavBucket struct {
	Nav  float64
	Idle float64
}

// NavReport NAV 口径单源结果（三消费面=deptreport/投资人页 IR/配置器 共读）。
// D-522：ByCcy 按结算币种分账（跨币永不加总）；NavUSD/IdleUSD=USD 桶别名兼容旧面。
type NavReport struct {
	NavUSD        float64              // = ByCcy["USD"].Nav（旧面兼容别名）
	IdleUSD       float64              // = ByCcy["USD"].Idle
	ByCcy         map[string]NavBucket // 币种分账面（USD/USDT/…；幽灵池不入）
	Pools         []ExplorerIdlePool
	SkippedNonUSD int // 非 USD/USDT 计价池计数（诚实口径不假换算）
	SkippedGhost  int // 幽灵池剔除计数（broker_accounts 移除户快照残行——不计 NAV 留痕）
	ComputedAt    time.Time
}

// SumNav 池集→NAV 聚合规则唯一实现（ComputeNav/测试 fake 同径复用）。
// D-522：USD/USDT 各归其桶（USDⓈ=同一记帐单位族不混桶——USD 桶只收 currency='USD' 池，
// USDT 桶只收 'USDT' 池，二者从不相加）。
// D-528：非 USD/USDT 池同样入 ByCcy 原生量桶（分币持仓入账——BTC 现货漏计实证；
// 各桶原生单位零换算，跨币永不加总）；SkippedNonUSD 留=非 USD/USDT 池计数（审计面）。
func SumNav(pools []ExplorerIdlePool, now time.Time) *NavReport {
	r := &NavReport{Pools: pools, ByCcy: map[string]NavBucket{}, ComputedAt: now}
	for _, p := range pools {
		if p.Ghost {
			r.SkippedGhost++
			continue
		}
		if p.Currency == "" {
			r.SkippedNonUSD++
			continue // 纯 '' 残影组不入桶（blank 合并已并主币行；残组无币名不立账）
		}
		if p.Currency != "USD" && p.Currency != "USDT" {
			r.SkippedNonUSD++
		}
		b := r.ByCcy[p.Currency]
		b.Nav += p.Equity
		b.Idle += p.Balance
		r.ByCcy[p.Currency] = b
	}
	if b, ok := r.ByCcy["USD"]; ok {
		r.NavUSD, r.IdleUSD = b.Nav, b.Idle
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

// NavDailyRow fin_nav_daily 行（D-522：PK=(day_start,currency) 分账——同日复算覆盖最新值）。
type NavDailyRow struct {
	DayStart   time.Time       `json:"-"`
	Currency   string          `json:"-"`
	NavUSD     float64         `json:"nav_usd"`
	IdleUSD    float64         `json:"idle_usd"`
	Venues     json.RawMessage `json:"-"`
	ComputedAt time.Time       `json:"-"`
}

// UpsertNavDaily 日切落行（PK=(day_start,currency) 冲突覆盖——幂等；venues=该币种池分解）。
func (s *Store) UpsertNavDaily(ctx context.Context, r NavDailyRow) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_nav_daily (day_start, currency, nav_usd, idle_usd, venues, computed_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (day_start, currency) DO UPDATE SET
			nav_usd=$3, idle_usd=$4, venues=$5, computed_at=$6`,
		r.DayStart, r.Currency, r.NavUSD, r.IdleUSD, r.Venues, r.ComputedAt)
	if err != nil {
		return fmt.Errorf("upsert nav_daily: %w", err)
	}
	return nil
}

// LatestNavDaily 最新日切全部币种行（D-522 分账面；缺行 len=0 非错误，读败 fail-closed）。
func (s *Store) LatestNavDaily(ctx context.Context) ([]NavDailyRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT day_start, currency, nav_usd, idle_usd, venues, computed_at
		FROM fin_nav_daily
		WHERE day_start = (SELECT max(day_start) FROM fin_nav_daily)
		ORDER BY currency`)
	if err != nil {
		return nil, fmt.Errorf("latest nav_daily: %w", err)
	}
	defer rows.Close()
	var out []NavDailyRow
	for rows.Next() {
		var r NavDailyRow
		if err := rows.Scan(&r.DayStart, &r.Currency, &r.NavUSD, &r.IdleUSD, &r.Venues, &r.ComputedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
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
