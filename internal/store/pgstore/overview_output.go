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
func (s *Store) OverviewFunnel(ctx context.Context, since time.Time) ([]OverviewFunnelRow, error) {
	count := func(q string, args ...any) (int, error) {
		var n int
		if err := s.pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
			return 0, err
		}
		return n, nil
	}
	get := func(stage, label, q string, args ...any) (OverviewFunnelRow, error) {
		n, err := count(q, args...)
		return OverviewFunnelRow{Stage: stage, Label: label, Count48h: n}, err
	}
	out := make([]OverviewFunnelRow, 0, 9)
	// 发现：新品类假设行产出。
	if r, err := get("discovery", "发现", `SELECT COUNT(*) FROM fin_opp_hypothesis WHERE first_seen >= $1`, since); err != nil {
		return nil, err
	} else {
		out = append(out, r)
	}
	// 评估：机会评估卡（computed_at）。
	if r, err := get("eval", "评估", `SELECT COUNT(*) FROM fin_opp_eval WHERE computed_at >= $1`, since); err != nil {
		return nil, err
	} else {
		out = append(out, r)
	}
	// 沙箱：纸面轨新开行（entry_ts）。
	if r, err := get("sandbox", "沙箱", `SELECT COUNT(*) FROM paper_spot_carry WHERE entry_ts >= $1`, since); err != nil {
		return nil, err
	} else {
		out = append(out, r)
	}
	// 执行：提案放行（executed+decided_at——dciexec/earn 双轨推进产物）。
	if r, err := get("execution", "执行", `SELECT COUNT(*) FROM fin_proposal WHERE status='executed' AND decided_at >= $1`, since); err != nil {
		return nil, err
	} else {
		out = append(out, r)
	}
	// 持仓：真金开仓。
	if r, err := get("holding", "持仓", `SELECT COUNT(*) FROM positions WHERE opened_at >= $1`, since); err != nil {
		return nil, err
	} else {
		out = append(out, r)
	}
	// 平仓：关仓。
	if r, err := get("exit", "平仓", `SELECT COUNT(*) FROM positions WHERE closed_at >= $1`, since); err != nil {
		return nil, err
	} else {
		out = append(out, r)
	}
	// 账本：结算行（net_realized 归账）。
	if r, err := get("ledger", "账本", `SELECT COUNT(*) FROM position_settlements WHERE settled_at >= $1`, since); err != nil {
		return nil, err
	} else {
		out = append(out, r)
	}
	// 复盘：裁定归因刷新（decided_at——物化裁定行）。
	if r, err := get("review", "复盘", `SELECT COUNT(*) FROM fin_decision_outcomes WHERE decided_at >= $1`, since); err != nil {
		return nil, err
	} else {
		out = append(out, r)
	}
	// 淘汰：fin_strategy 无生命周期事件时戳（decided_at=末次裁定）→ retired 累计数如实标注。
	var elim int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM fin_strategy WHERE state='retired'`).Scan(&elim); err != nil {
		return nil, fmt.Errorf("overview funnel elimination: %w", err)
	}
	out = append(out, OverviewFunnelRow{Stage: "elimination", Label: "淘汰", Count48h: elim, Cumulative: true})
	return out, nil
}
