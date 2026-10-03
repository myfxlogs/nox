package pgstore

import (
	"context"
	"fmt"
	"time"
)

// risk_lines.go —— D-032（decisions D-030 风控硬线）：账本回撤/事故仓/venue 名义读口。
// 事故族谓词复用 incidentPositionPredicate（position_net.go 单源，别名约定 p/s 同此文件）。

// NetRealizedSince 滚动窗账本净额=Σ position_settlements（closed_at ≥ since，含事故仓——
// 风控线不剔样本；excludeIncidents=true 时剔事故族供周报「非事故样本」列）。
// net 三级 fallback 同 DailyRealizedPnl（net_realized_usd→realized_usd→realized_pnl）：
// 无 settlement 的 legacy 行按 realized_pnl 计，缺数据静默归零=哑数据击穿风控（D-223 R1）。
func (s *Store) NetRealizedSince(ctx context.Context, since time.Time, excludeIncidents bool) (float64, error) {
	excl := ""
	if excludeIncidents {
		excl = " AND NOT " + incidentPositionPredicate
	}
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

// IncidentBlockerID 未 ack 事故仓最小 id（0=无）。ack 游标=ARB_RISK_INCIDENT_ACK_ID
// 之外的仓恒计数（LEGGED/FAILED 终态 + 事故族 CLOSED 一并覆盖，决策席归档 D# 后推 id 放行）。
func (s *Store) IncidentBlockerID(ctx context.Context, ackID int64) (int64, error) {
	var id *int64
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT MIN(p.id)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE p.id > $1 AND %s`, incidentPositionPredicate), ackID).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("incident blocker id: %w", err)
	}
	if id == nil {
		return 0, nil
	}
	return *id, nil
}

// IncidentCountSince 事故仓计数（周报「事故数」列）。未平仓事故（LEGGED/FAILED，closed_at 空）
// 按 opened_at 落窗；COALESCE 取两时点中有效者。
func (s *Store) IncidentCountSince(ctx context.Context, since time.Time) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(DISTINCT p.id)
		FROM positions p
		LEFT JOIN position_settlements s ON s.position_id = p.id
		WHERE %s AND COALESCE(p.closed_at, p.opened_at) >= $1`, incidentPositionPredicate), since).Scan(&n)
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
