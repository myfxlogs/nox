// strategy_retire.go —— 策略退役判据读面（oppmatch retire 委任径用）。
// 判据=单 SQL 聚合：N 日内 fin_proposal.template_id / autotrade_logs.strategy /
// income_events（positions 链归属）任一活动 → 非退休格；持仓中（OPEN 系状态）
// 永不退。零活动∧累计 total_pnl≤floor → '亏损硬线'；零活动∧零持仓 → '闲置 Nd'。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// StrategyRetireVerdict 返回退役命中因（""=不退）。在飞提案/在持仓前置挡。
func (s *Store) StrategyRetireVerdict(ctx context.Context, strategy string,
	idle time.Duration, pnlFloor float64) (string, error) {
	var props, acts, opens, incomes int
	var totalPnl float64
	err := s.pool.QueryRow(ctx, `
		SELECT
		  (SELECT count(*) FROM fin_proposal p WHERE p.template_id=$1 AND p.created_at > now()-$2::interval),
		  (SELECT count(*) FROM autotrade_logs a WHERE a.strategy=$1 AND a.ts > now()-$2::interval),
		  (SELECT count(*) FROM positions p WHERE p.type=$1
		     AND p.status IN ('OPENING','OPEN','LEGGED','CLOSING')),
		  (SELECT count(*) FROM income_events i JOIN positions p ON i.position_id=p.id
		     WHERE p.type=$1 AND i.ts > now()-$2::interval),
		  COALESCE((SELECT total_pnl FROM strategy_stats ss WHERE ss.strategy=$1),0)`,
		strategy, fmt.Sprintf("%f hours", idle.Hours())).
		Scan(&props, &acts, &opens, &incomes, &totalPnl)
	if err != nil {
		return "", fmt.Errorf("retire verdict %s: %w", strategy, err)
	}
	if opens > 0 {
		return "", nil // 在持仓永不自动退
	}
	if props+acts+incomes > 0 {
		return "", nil // N 日有活动不误伤
	}
	if totalPnl <= pnlFloor {
		return fmt.Sprintf("累计 PnL %.2f 越亏损硬线 %.2f 且零持仓", totalPnl, pnlFloor), nil
	}
	return fmt.Sprintf("live 零提案零动作零收益 %.0fd（累计 PnL %.2f）",
		idle.Hours()/24, totalPnl), nil
}
