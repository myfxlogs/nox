// overview_output.go —— 产出面补建（总览 §6 归因分解+§7 闭环漏斗——后端聚合，
// 前端零运算只渲染）。
package pgstore

import (
	"context"
	"fmt"
	"time"
)

// OverviewAttribRow 收益归因行（策略键×30d 结算净额）。
type OverviewAttribRow struct {
	Strategy  string
	Positions int
	Net30USD  float64
	Wins      int
	Losses    int
}

// OverviewAttribution 净额按策略归因：settlements JOIN positions，
// GROUP BY COALESCE(strategy_key, type)（未归组行落引擎 type 名——归因面
// 如实披露不丢行）。窗口=settled_at ≥ since。排序 net 降序由调用方组装面定。
func (s *Store) OverviewAttribution(ctx context.Context, since time.Time) ([]OverviewAttribRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(NULLIF(p.strategy_key, ''), 'unassigned:'||p.type) AS strategy,
		       COUNT(*) AS positions,
		       COALESCE(SUM(st.net_realized_usd), 0) AS net30,
		       COUNT(*) FILTER (WHERE st.net_realized_usd > 0) AS wins,
		       COUNT(*) FILTER (WHERE st.net_realized_usd < 0) AS losses
		FROM position_settlements st
		JOIN positions p ON p.id = st.position_id
		WHERE st.settled_at >= $1
		GROUP BY 1
		ORDER BY net30 DESC`, since)
	if err != nil {
		return nil, fmt.Errorf("overview attribution: %w", err)
	}
	defer rows.Close()
	var out []OverviewAttribRow
	for rows.Next() {
		var r OverviewAttribRow
		if err := rows.Scan(&r.Strategy, &r.Positions, &r.Net30USD, &r.Wins, &r.Losses); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// OverviewFunnelRow 闭环漏斗环计数。
type OverviewFunnelRow struct {
	Stage      string
	Label      string
	Count48h   int
	Cumulative bool // 环缺 48h 时戳列按累计计数（如实标注）
}

// OverviewFunnel 闭环八环节点 48h 计数（发现→评估→沙箱→执行→持仓→平仓→账本→复盘→淘汰）。
// 各环节点表实读（COUNT 非投影——决策席工作流审计注记同口径窗）。
// 九环并单 round-trip（UNION ALL）：旧版 9 条串行 COUNT 挤 10 连接池，
// prefetch 并发期实测 8s+；合一后一跳出齐（环序=labels 表序非 SQL 序——
// UNION ALL 不保序，按 labels 键回排）。
func (s *Store) OverviewFunnel(ctx context.Context, since time.Time) ([]OverviewFunnelRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT 'discovery', COUNT(*) FROM fin_opp_hypothesis WHERE first_seen >= $1
		UNION ALL SELECT 'eval', COUNT(*) FROM fin_opp_eval WHERE computed_at >= $1
		UNION ALL SELECT 'sandbox', COUNT(*) FROM paper_spot_carry WHERE entry_ts >= $1
		UNION ALL SELECT 'execution', COUNT(*) FROM fin_proposal WHERE status='executed' AND decided_at >= $1
		UNION ALL SELECT 'holding', COUNT(*) FROM positions WHERE opened_at >= $1
		UNION ALL SELECT 'exit', COUNT(*) FROM positions WHERE closed_at >= $1
		UNION ALL SELECT 'ledger', COUNT(*) FROM position_settlements WHERE settled_at >= $1
		UNION ALL SELECT 'review', COUNT(*) FROM fin_decision_outcomes WHERE decided_at >= $1
		UNION ALL SELECT 'elimination', COUNT(*) FROM fin_strategy WHERE state='retired'`, since)
	if err != nil {
		return nil, fmt.Errorf("overview funnel: %w", err)
	}
	defer rows.Close()
	counts := map[string]int{}
	for rows.Next() {
		var stage string
		var n int
		if err := rows.Scan(&stage, &n); err != nil {
			return nil, err
		}
		counts[stage] = n
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	out := make([]OverviewFunnelRow, 0, 9)
	for _, st := range funnelStages {
		out = append(out, OverviewFunnelRow{
			Stage: st.key, Label: st.label, Count48h: counts[st.key], Cumulative: st.cumulative,
		})
	}
	return out, nil
}

// funnelStages 环序+标签+累计标（环序=业务闭环序由调用方消费——SQL 序无关）。
var funnelStages = []struct {
	key, label string
	cumulative bool
}{
	{"discovery", "发现", false},
	{"eval", "评估", false},
	{"sandbox", "沙箱", false},
	{"execution", "执行", false},
	{"holding", "持仓", false},
	{"exit", "平仓", false},
	{"ledger", "账本", false},
	{"review", "复盘", false},
	{"elimination", "淘汰", true},
}
