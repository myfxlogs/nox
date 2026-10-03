// probe.go —— D-047：系统探针仓（type='PROBE'）专用查询面。
// 与既有仓复用 positions/position_legs/position_cashflows 三表；
// venue_key = legs.venue || ':' || legs.venue_type（CreatePositionLeg 拆键落库 → 读出重组）。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PositionTypePROBE D-047 探针仓类型常量（positions_type_check 八值其一，migration 0175）。
const PositionTypePROBE = "PROBE"

// probeExclusionPredicate D-047④：PROBE 仓排除谓词——现金流入 D-013 账本但
// 策略表现/淘汰/rollup/dd30 统计一律不计（同 incident 排除口径，SQL 层 WHERE 拼点）。
// 别名约定同 incidentPositionPredicate（p=positions）。
const probeExclusionPredicate = `p.type <> 'PROBE'`

// probeActiveStatuses 在途探针仓状态集（OPENING→CLOSING 全链——在途口径同 ACTIVE 语义）。
var probeActiveStatuses = []string{"OPENING", "OPEN", "LEGGED", "CLOSING"}

// ProbeRow ListProbes/到期扫描行（position + 首 OPEN 腿 venue/symbol/side/qty）。
type ProbeRow struct {
	Position
	VenueKey string
	Symbol   string
	Side     string
	LegQty   float64
	OrderID  string
}

// CountActiveProbes 在途探针仓数（守卫：≤2 上限判定用）。
func (s *Store) CountActiveProbes(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM positions WHERE type='PROBE' AND status = ANY($1)`,
		probeActiveStatuses).Scan(&n); err != nil {
		return 0, fmt.Errorf("count active probes: %w", err)
	}
	return n, nil
}

// ListDueProbes 到期在仓探针（type='PROBE' 且 OPEN 且 auto_close_at ≤ now）。
// OPENING/CLOSING 不取（开仓途中/平仓途中非可平态；LEGGED 理论上单腿即 OPEN 态）。
func (s *Store) ListDueProbes(ctx context.Context, now time.Time) ([]ProbeRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.opportunity_id, p.type, p.status, p.note, p.notional_usd,
		       p.decision_ref, p.auto_close_at, p.opened_at, p.closed_at, COALESCE(p.exit_kind,''),
		       l.venue || ':' || l.venue_type, l.symbol, l.side, l.qty, l.order_id
		FROM positions p
		LEFT JOIN position_legs l ON l.position_id = p.id AND l.leg_no = 1 AND l.phase = 'OPEN'
		WHERE p.type = 'PROBE' AND p.status = 'OPEN' AND p.auto_close_at IS NOT NULL AND p.auto_close_at <= $1
		ORDER BY p.id`, now)
	if err != nil {
		return nil, fmt.Errorf("list due probes: %w", err)
	}
	defer rows.Close()
	return scanProbeRows(rows)
}

// ListStuckDueProbes D-047 R1④：到期但卡在非可平态的探针（OPENING/CLOSING）——
// venue 侧未确认/平仓在途，系统只告警不擅动状态（待对账/人工收编）。
func (s *Store) ListStuckDueProbes(ctx context.Context, now time.Time) ([]ProbeRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.opportunity_id, p.type, p.status, p.note, p.notional_usd,
		       p.decision_ref, p.auto_close_at, p.opened_at, p.closed_at, COALESCE(p.exit_kind,''),
		       l.venue || ':' || l.venue_type, l.symbol, l.side, l.qty, l.order_id
		FROM positions p
		LEFT JOIN position_legs l ON l.position_id = p.id AND l.leg_no = 1 AND l.phase = 'OPEN'
		WHERE p.type = 'PROBE' AND p.status IN ('OPENING','CLOSING')
		  AND p.auto_close_at IS NOT NULL AND p.auto_close_at <= $1
		ORDER BY p.id`, now)
	if err != nil {
		return nil, fmt.Errorf("list stuck due probes: %w", err)
	}
	defer rows.Close()
	return scanProbeRows(rows)
}

// ListProbes 全量探针仓（openOnly=true 只在途）。前端/巡检读面。
func (s *Store) ListProbes(ctx context.Context, openOnly bool) ([]ProbeRow, error) {
	q := `
		SELECT p.id, p.opportunity_id, p.type, p.status, p.note, p.notional_usd,
		       p.decision_ref, p.auto_close_at, p.opened_at, p.closed_at, COALESCE(p.exit_kind,''),
		       l.venue || ':' || l.venue_type, l.symbol, l.side, l.qty, l.order_id
		FROM positions p
		LEFT JOIN position_legs l ON l.position_id = p.id AND l.leg_no = 1 AND l.phase = 'OPEN'`
	if openOnly {
		q += ` WHERE p.type='PROBE' AND p.status = ANY(ARRAY['OPENING','OPEN','LEGGED','CLOSING'])`
	} else {
		q += ` WHERE p.type='PROBE'`
	}
	q += ` ORDER BY p.id DESC LIMIT 200`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list probes: %w", err)
	}
	defer rows.Close()
	return scanProbeRows(rows)
}

func scanProbeRows(rows pgx.Rows) ([]ProbeRow, error) {
	var out []ProbeRow
	for rows.Next() {
		var r ProbeRow
		var venueKey, symbol, side, orderID *string
		var legQty *float64
		if err := rows.Scan(&r.ID, &r.OpportunityID, &r.Type, &r.Status, &r.Note, &r.NotionalUSD,
			&r.DecisionRef, &r.AutoCloseAt, &r.OpenedAt, &r.ClosedAt, &r.ExitKind,
			&venueKey, &symbol, &side, &legQty, &orderID); err != nil {
			return nil, err
		}
		if venueKey != nil {
			r.VenueKey = *venueKey
		}
		if symbol != nil {
			r.Symbol = *symbol
		}
		if side != nil {
			r.Side = *side
		}
		if legQty != nil {
			r.LegQty = *legQty
		}
		if orderID != nil {
			r.OrderID = *orderID
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ProbeCashflowSumSince 探针仓 30d 现金流合计（selfwatch「探针成本」行源）。
// Σ position_cashflows（全 kind：commission+funding+swap+price 残差流）。
func (s *Store) ProbeCashflowSumSince(ctx context.Context, since time.Time) (float64, error) {
	var sum *float64
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(cf.amount_usd), 0)
		FROM position_cashflows cf
		JOIN positions p ON p.id = cf.position_id
		WHERE p.type = 'PROBE' AND cf.ts >= $1`, since).Scan(&sum); err != nil {
		return 0, fmt.Errorf("probe cashflows: %w", err)
	}
	if sum == nil {
		return 0, nil
	}
	return *sum, nil
}

// ProbeMeta broker 持仓页探针标注用元数据（decision_ref + auto_close_at）。
type ProbeMeta struct {
	DecisionRef string
	AutoCloseAt *time.Time
}

// ProbeMetaByPositionIDs 批量查探针元数据（ListBrokerPositions 标注路径）。
// 非 PROBE/缺席 id 不在返回 map（调用方按命中标注）。
func (s *Store) ProbeMetaByPositionIDs(ctx context.Context, ids []int64) (map[int64]ProbeMeta, error) {
	out := make(map[int64]ProbeMeta, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(decision_ref,''), auto_close_at
		FROM positions WHERE type='PROBE' AND id = ANY($1)`, ids)
	if err != nil {
		return nil, fmt.Errorf("probe meta: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var m ProbeMeta
		if err := rows.Scan(&id, &m.DecisionRef, &m.AutoCloseAt); err != nil {
			return nil, err
		}
		out[id] = m
	}
	return out, rows.Err()
}
