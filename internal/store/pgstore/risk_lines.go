package pgstore

import (
	"context"
	"fmt"
	"time"
)

// risk_lines.go —— D-032（decisions D-030 风控硬线）：账本回撤/事故仓/venue 名义读口。
// 事故闸用专用谓词 incidentGatePredicate（D-032 R1）；D-013 统计/账本排除口径仍用
// 共享 incidentPositionPredicate（position_net.go 单源，别名约定 p/s 同此文件）。

// incidentGatePredicate 事故闸专用谓词（D-032 R1）：与共享 incidentPositionPredicate 分家——
// 全腿零成交的 FAILED 是 maker 撤单/全腿被拒的零敞口干净终态（D-018 首腿撤单设计），非事故；
// FAILED 仅当 ∃ FILLED 腿（真成交过=裸露过）才计。exit_kind/incident_attributed/LEGGED 不变。
const incidentGatePredicate = `(COALESCE(p.exit_kind,'') IN ('legged_unwind','incident_close') OR COALESCE(s.incident_attributed,false) OR p.status='LEGGED' OR (p.status='FAILED' AND EXISTS (SELECT 1 FROM position_legs gl WHERE gl.position_id=p.id AND gl.status='FILLED')))`

// NetRealizedSince 滚动窗账本净额=Σ position_settlements（closed_at ≥ since，含事故仓——
// 风控线不剔样本；excludeIncidents=true 时剔事故族供周报「非事故样本」列）。
// net 三级 fallback 同 DailyRealizedPnl（net_realized_usd→realized_usd→realized_pnl）：
// 无 settlement 的 legacy 行按 realized_pnl 计，缺数据静默归零=哑数据击穿风控（D-223 R1）。
func (s *Store) NetRealizedSince(ctx context.Context, since time.Time, excludeIncidents bool) (float64, error) {
	excl := ""
	if excludeIncidents {
		excl = " AND NOT " + incidentPositionPredicate
	}
	// D-047④：PROBE 仓恒排除（dd30 统计口径；现金流入账本但不计业绩/风控聚合）。
	excl += " AND " + probeExclusionPredicate
	var sum *float64
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COALESCE(SUM(COALESCE(s.net_realized_usd, s.realized_usd, p.realized_pnl)), 0)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE p.status = 'CLOSED' AND p.closed_at >= $1%s`, excl), since).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("net realized since %s: %w", since, err)
	}
	if sum == nil {
		return 0, nil
	}
	return *sum, nil
}

// IncidentBlockerID 未 ack 事故仓最小 id（0=无）。谓词=incidentGatePredicate（D-032 R1）：
// LEGGED + 有成交腿的 FAILED + 事故族 CLOSED 覆盖；ack 游标=NOX_RISK_INCIDENT_ACK_ID，
// 决策席归档 D# 后推 id 放行。
func (s *Store) IncidentBlockerID(ctx context.Context, ackID int64) (int64, error) {
	var id *int64
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT MIN(p.id)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE p.id > $1 AND %s`, incidentGatePredicate), ackID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("incident blocker id: %w", err)
	}
	if id == nil {
		return 0, nil
	}
	return *id, nil
}

// IncidentCountSince 事故仓计数（周报「事故数」列，同 incidentGatePredicate 口径）。
// 未平仓事故（LEGGED/FAILED，closed_at 空）按 opened_at 落窗；COALESCE 取两时点中有效者。
func (s *Store) IncidentCountSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(DISTINCT p.id)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE %s AND COALESCE(p.closed_at, p.opened_at) >= $1`, incidentGatePredicate), since).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("incident count since %s: %w", since, err)
	}
	return n, nil
}

// VenueNotionalExposure venueKey(venue_key 生成列)→在仓名义映射（D-032 ③ 权益纪律闸）。
// 口径：p.status='OPEN' 仓 p.type∈types 的 OPEN 腿，腿名义=notional_usd/该仓 OPEN 腿数
// （notional_usd=双腿口径 D-179，均分摊到各腿 venue；notional_usd NULL 腿计 0——
// NULL 仓位低估敞口，注释留痕不猜价）。
func (s *Store) VenueNotionalExposure(ctx context.Context, types []string) (map[string]float64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT l.venue_key, COALESCE(SUM(COALESCE(p.notional_usd, 0) / NULLIF(cnt.cnt, 0)), 0)
		FROM position_legs l
		JOIN positions p ON p.id = l.position_id
		JOIN (SELECT position_id, COUNT(*) AS cnt FROM position_legs
		      WHERE phase = 'OPEN' GROUP BY position_id) cnt ON cnt.position_id = p.id
		WHERE l.phase = 'OPEN' AND p.status = 'OPEN' AND p.type = ANY($1)
		GROUP BY l.venue_key`, types)
	if err != nil {
		return nil, fmt.Errorf("venue notional exposure: %w", err)
	}
	defer rows.Close()
	out := make(map[string]float64)
	for rows.Next() {
		var venue string
		var v float64
		if err := rows.Scan(&venue, &v); err != nil {
			return nil, fmt.Errorf("venue notional exposure scan: %w", err)
		}
		out[venue] = v
	}
	return out, rows.Err()
}
