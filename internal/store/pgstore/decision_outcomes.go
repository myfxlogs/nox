// decision_outcomes.go —— D-561 裁定归因簿（决策席问责机制②系统件）。
// 物化=扫描幂等（UNIQUE 守卫）：裁定源行（carry_graduations approved /
// fin_proposal executed）自带全部决策事实，单漏斗零漏路径，首拍回填存量行。
// 归因=裁定对象关联其后 position_settlements 净额滚动 30d（呈现非裁决——
// 禁自动改判据/禁自动禁用模板，只武装问责）；连败/净亏→warn 边沿推送。
package pgstore

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"
)

// 决策归因阈值（D-561 票面定）：30d 归因累计净亏 或 连败 N 笔 → warn（边沿触发）。
const (
	DecisionLossStreakThreshold = 3
	decisionAttribWindow        = 30 * 24 * time.Hour
)

// DecisionOutcome 裁定归因行。
type DecisionOutcome struct {
	ID          int64
	Category    string
	ObjectKey   string
	Title       string
	DecisionRef string
	StrategyKey string
	Canonical   string
	Attrs       map[string]any
	DecidedAt   time.Time
	// 归因快照（runner 刷新；nil=未算）
	AttribNetUSD *float64
	AttribPos    *int
	AttribWins   *int
	AttribLosses *int
	AttribStreak *int
	AlertState   string
	AlertAt      *time.Time
}

// MaterializeDecisionOutcomes 扫描裁定源行物化归因簿（幂等；返回新落行数）。
// ① carry_graduations approved：strategy_key=structure→fin_position_strategy_map
// （D-554 判据表；无行=”=模板级归因关闭，行仍落）；canonical=Symbol。
// ② fin_proposal executed：strategy_key=template_id（提案模板即归组域）。
func (s *Store) MaterializeDecisionOutcomes(ctx context.Context) (int, error) {
	n := 0
	rows, err := s.pool.Query(ctx, `
		SELECT id, symbol, structure, venue_a, venue_b, COALESCE(mt_venue,''),
		       COALESCE(decided_by,''), COALESCE(combo_id, 0), decided_at, decided_note
		FROM carry_graduations WHERE status='approved'`)
	if err != nil {
		return n, fmt.Errorf("scan approved graduations: %w", err)
	}
	type gradRow struct {
		id                                      int64
		symbol, structure, va, vb, mt, by, note string
		comboID                                 int64
		decidedAt                               time.Time
	}
	var grads []gradRow
	for rows.Next() {
		var g gradRow
		if err := rows.Scan(&g.id, &g.symbol, &g.structure, &g.va, &g.vb, &g.mt,
			&g.by, &g.comboID, &g.decidedAt, &g.note); err != nil {
			rows.Close()
			return n, err
		}
		grads = append(grads, g)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return n, err
	}
	for _, g := range grads {
		var strategyKey string
		_ = s.pool.QueryRow(ctx,
			`SELECT strategy_key FROM fin_position_strategy_map WHERE source_kind='structure' AND source_val=$1`,
			g.structure).Scan(&strategyKey) // 无行=''（模板级归因关闭），真错同态=呈现面可忍
		title := fmt.Sprintf("grad#%d %s %s×%s", g.id, g.symbol, g.va, g.vb)
		if g.mt != "" {
			title += "×" + g.mt
		}
		by := g.by
		if by == "" {
			by = "committee_rpc"
		}
		inserted, err := s.insertDecisionOutcome(ctx, &DecisionOutcome{
			Category: "graduation_approved", ObjectKey: fmt.Sprintf("grad:%d", g.id),
			Title: title, DecisionRef: by, StrategyKey: strategyKey, Canonical: g.symbol,
			Attrs: map[string]any{"structure": g.structure, "combo_id": g.comboID,
				"note": g.note, "venue_a": g.va, "venue_b": g.vb, "mt_venue": g.mt},
			DecidedAt: g.decidedAt,
		})
		if err != nil {
			return n, err
		}
		if inserted {
			n++
		}
	}
	// ② 提案放行→真金执行（dciexec SetProposalStatus executed 两写点产物）。
	rows, err = s.pool.Query(ctx, `
		SELECT id, template_id, exec_track, venue, COALESCE(expected_apr,0), decided_at
		FROM fin_proposal WHERE status='executed' AND decided_at IS NOT NULL`)
	if err != nil {
		return n, fmt.Errorf("scan executed proposals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var tmpl, track, venue string
		var apr float64
		var decidedAt time.Time
		if err := rows.Scan(&id, &tmpl, &track, &venue, &apr, &decidedAt); err != nil {
			return n, err
		}
		inserted, err := s.insertDecisionOutcome(ctx, &DecisionOutcome{
			Category: "proposal_go", ObjectKey: fmt.Sprintf("proposal:%d", id),
			Title:       fmt.Sprintf("proposal#%d %s@%s", id, tmpl, venue),
			DecisionRef: "dciexec", StrategyKey: tmpl,
			Attrs:     map[string]any{"exec_track": track, "venue": venue, "expected_apr": apr},
			DecidedAt: decidedAt,
		})
		if err != nil {
			return n, err
		}
		if inserted {
			n++
		}
	}
	return n, rows.Err()
}

func (s *Store) insertDecisionOutcome(ctx context.Context, o *DecisionOutcome) (bool, error) {
	attrs, err := json.Marshal(o.Attrs)
	if err != nil {
		return false, fmt.Errorf("attrs %s/%s: %w", o.Category, o.ObjectKey, err)
	}
	tag, err := s.pool.Exec(ctx, `
		INSERT INTO fin_decision_outcomes (category, object_key, title, decision_ref, strategy_key, canonical, attrs, decided_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
		ON CONFLICT (category, object_key) DO NOTHING`,
		o.Category, o.ObjectKey, o.Title, o.DecisionRef, o.StrategyKey, o.Canonical, attrs, o.DecidedAt)
	if err != nil {
		return false, fmt.Errorf("insert outcome %s/%s: %w", o.Category, o.ObjectKey, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ListDecisionOutcomes 归因簿全量（decided_at 倒序；runner 与读面共用）。
func (s *Store) ListDecisionOutcomes(ctx context.Context) ([]DecisionOutcome, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, category, object_key, title, decision_ref, strategy_key, canonical,
		       COALESCE(attrib_net_usd, 0), COALESCE(attrib_positions, 0), COALESCE(attrib_wins, 0),
		       COALESCE(attrib_losses, 0), COALESCE(attrib_streak, 0), alert_state, alert_at, decided_at
		FROM fin_decision_outcomes ORDER BY decided_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DecisionOutcome
	for rows.Next() {
		var o DecisionOutcome
		var net float64
		var pos, wins, losses, streak int
		if err := rows.Scan(&o.ID, &o.Category, &o.ObjectKey, &o.Title, &o.DecisionRef,
			&o.StrategyKey, &o.Canonical, &net, &pos, &wins, &losses, &streak,
			&o.AlertState, &o.AlertAt, &o.DecidedAt); err != nil {
			return nil, err
		}
		o.AttribNetUSD, o.AttribPos, o.AttribWins, o.AttribLosses, o.AttribStreak = &net, &pos, &wins, &losses, &streak
		out = append(out, o)
	}
	return out, rows.Err()
}

// RefreshDecisionOutcomeAttribution 归因快照刷新+告警边沿（runner 周期调用）。
// 关联判据：positions 开仓∈[decided_at, decided_at+30d] 且 strategy_key 匹配
// （canonical 非空再窄化 opportunity_id 段精确匹配）；结算 settled_at∈其后窗。
// 返回本次推警条数。
func (s *Store) RefreshDecisionOutcomeAttribution(ctx context.Context) (int, error) {
	outcomes, err := s.ListDecisionOutcomes(ctx)
	if err != nil {
		return 0, err
	}
	alerts := 0
	for _, o := range outcomes {
		net, pos, wins, losses, streak, err := s.decisionAttrib(ctx, o)
		if err != nil {
			log.Printf("[decision-attrib] %s/%s: %v", o.Category, o.ObjectKey, err)
			continue
		}
		newState := attribAlertState(net, streak)
		if _, err := s.pool.Exec(ctx, `
			UPDATE fin_decision_outcomes SET attrib_net_usd=$2, attrib_positions=$3,
			       attrib_wins=$4, attrib_losses=$5, attrib_streak=$6, alert_state=$7,
			       alert_at=CASE WHEN $7<>'' THEN now() ELSE alert_at END
			WHERE id=$1`, o.ID, net, pos, wins, losses, streak, newState); err != nil {
			return alerts, fmt.Errorf("update attrib %s/%s: %w", o.Category, o.ObjectKey, err)
		}
		// 边沿推送：进入亏损/连败态或恶化升级才 warn（D-389 边沿先例；禁线=呈现非裁决）。
		if newState != "" && newState != o.AlertState {
			if err := s.InsertAlert(ctx, nil, "warn", decisionAlertText(o, net, pos, losses, streak)); err != nil {
				log.Printf("[decision-attrib] alert %s/%s: %v", o.Category, o.ObjectKey, err)
				continue
			}
			alerts++
		}
	}
	return alerts, nil
}

// decisionAttrib 单裁定 30d 滚动归因（净额/仓数/胜负/负连败）。
// settled_at 倒序返回——负连败=自最新往回连续 net<0 计数。
func (s *Store) decisionAttrib(ctx context.Context, o DecisionOutcome) (net float64, pos, wins, losses, streak int, err error) {
	rows, err := s.pool.Query(ctx, `
		SELECT COALESCE(st.net_realized_usd, 0)
		FROM positions p
		JOIN position_settlements st ON st.position_id = p.id AND st.settled_at >= $1
		WHERE p.opened_at >= $1 AND p.opened_at < $2
		  AND ($3='' OR p.strategy_key=$3)
		  AND ($4='' OR p.opportunity_id LIKE '%:'||$4||':%')
		ORDER BY st.settled_at DESC`,
		o.DecidedAt, o.DecidedAt.Add(decisionAttribWindow), o.StrategyKey, o.Canonical)
	if err != nil {
		return 0, 0, 0, 0, 0, err
	}
	defer rows.Close()
	var nets []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return 0, 0, 0, 0, 0, err
		}
		net += v
		pos++
		switch {
		case v > 0:
			wins++
		case v < 0:
			losses++
		}
		nets = append(nets, v)
	}
	if err := rows.Err(); err != nil {
		return 0, 0, 0, 0, 0, err
	}
	for _, v := range nets {
		if v >= 0 {
			break
		}
		streak++
	}
	return net, pos, wins, losses, streak, nil
}

// attribAlertState 亏损/连败态机：”/ok 无险；net_loss/streak 单险；组合升序拼接。
func attribAlertState(net float64, streak int) string {
	state := ""
	if net < 0 {
		state = "net_loss"
	}
	if streak >= DecisionLossStreakThreshold {
		if state != "" {
			state += "+"
		}
		state += "streak"
	}
	return state
}

func decisionAlertText(o DecisionOutcome, net float64, pos, losses, streak int) string {
	msg := fmt.Sprintf("裁定归因 warn：%s（%s）30d 净额 $%.2f / %d 仓", o.Title, o.DecisionRef, net, pos)
	if streak >= DecisionLossStreakThreshold {
		msg += fmt.Sprintf("，连败 %d 笔", streak)
	}
	return msg
}
