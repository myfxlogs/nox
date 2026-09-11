package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// Opportunity 对应 opportunities 表（§2.6）。
type Opportunity struct {
	ID             string
	Type           string
	Canonical      string
	VenueBuy       string
	VenueSell      string
	PriceBuy       float64
	PriceSell      float64
	GrossBps       float64
	SpreadCost     float64
	CommissionCost float64
	SlippageCost   float64
	SwapCost       float64
	NetBps         float64
	BreakevenDays  *float64
	QuoteTime      time.Time
	ExpiresAt      time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// UpsertOpportunity 插入或刷新机会（D-080 S2：组合键=PK，同组合刷新同行）。
// 返回 inserted=true 表示本次是新插入（首次出现），false 表示刷新既有行（D-050 站内推送判定）。
// PostgreSQL UPSERT 技巧：RETURNING (xmax = 0) —— 本语句实际执行 INSERT（新行）时 xmax=0，
// 触发 ON CONFLICT DO UPDATE（刷新既有行）时 xmax≠0。
// D-080：id=组合键（6 段，无时间戳），同组合每 tick 刷新同行（last_seen=now()）。
func (s *Store) UpsertOpportunity(ctx context.Context, o *Opportunity) (bool, error) {
	q := `INSERT INTO opportunities (
			id, type, canonical, venue_buy, venue_sell, price_buy, price_sell,
			gross_bps, spread_cost, commission_cost, slippage_cost, swap_cost,
			net_bps, breakeven_days, quote_time, expires_at, created_at, updated_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,now(),now())
		ON CONFLICT (id) DO UPDATE SET
			canonical=EXCLUDED.canonical,
			price_buy=EXCLUDED.price_buy, price_sell=EXCLUDED.price_sell,
			gross_bps=EXCLUDED.gross_bps, spread_cost=EXCLUDED.spread_cost,
			commission_cost=EXCLUDED.commission_cost, slippage_cost=EXCLUDED.slippage_cost,
			swap_cost=EXCLUDED.swap_cost, net_bps=EXCLUDED.net_bps,
			breakeven_days=EXCLUDED.breakeven_days, quote_time=EXCLUDED.quote_time,
			expires_at=EXCLUDED.expires_at, updated_at=now(), last_seen=now()
		RETURNING (xmax = 0) AS inserted`
	var inserted bool
	if err := s.pool.QueryRow(ctx, q,
		o.ID, o.Type, o.Canonical, o.VenueBuy, o.VenueSell, o.PriceBuy, o.PriceSell,
		o.GrossBps, o.SpreadCost, o.CommissionCost, o.SlippageCost, o.SwapCost,
		o.NetBps, o.BreakevenDays, o.QuoteTime, o.ExpiresAt).Scan(&inserted); err != nil {
		return false, fmt.Errorf("upsert opportunity: %w", err)
	}
	return inserted, nil
}

// ListOpportunities 返回未过期机会（§8.2）。
func (s *Store) ListOpportunities(ctx context.Context, typ string, limit, offset int) ([]Opportunity, error) {
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id, type, canonical, venue_buy, venue_sell, price_buy, price_sell,
			gross_bps, spread_cost, commission_cost, slippage_cost, swap_cost,
			net_bps, breakeven_days, quote_time, expires_at, created_at, updated_at
		FROM opportunities
		WHERE expires_at > now()
		  AND ($1 = '' OR type = $1)
		ORDER BY net_bps DESC
		LIMIT $2 OFFSET $3`
	rows, err := s.pool.Query(ctx, q, typ, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list opportunities: %w", err)
	}
	defer rows.Close()
	return scanOpportunities(rows)
}

// GetOpportunity D-091 分析员 get_entity：按 ID 读单条机会（含已过期）。不存在→nil, nil。
func (s *Store) GetOpportunity(ctx context.Context, id string) (*Opportunity, error) {
	q := `SELECT id, type, canonical, venue_buy, venue_sell, price_buy, price_sell,
			gross_bps, spread_cost, commission_cost, slippage_cost, swap_cost,
			net_bps, breakeven_days, quote_time, expires_at, created_at, updated_at
		FROM opportunities WHERE id = $1`
	var o Opportunity
	err := s.pool.QueryRow(ctx, q, id).Scan(
		&o.ID, &o.Type, &o.Canonical, &o.VenueBuy, &o.VenueSell, &o.PriceBuy, &o.PriceSell,
		&o.GrossBps, &o.SpreadCost, &o.CommissionCost, &o.SlippageCost, &o.SwapCost,
		&o.NetBps, &o.BreakevenDays, &o.QuoteTime, &o.ExpiresAt, &o.CreatedAt, &o.UpdatedAt)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get opportunity %s: %w", id, err)
	}
	return &o, nil
}

// Alert 对应 alerts 表（§2.5）。
type Alert struct {
	ID       int64
	RuleID   *int64
	RuleName string
	Ts       time.Time
	Level    string
	Message  string
	Acked    bool
}

// InsertAlert 写入告警（§2.5）。
// D-069：alertPaused=true 时静默丢弃（demo 阶段避免刷屏）。
// D-114：写入成功后回调 alertHook（main.go 注入 alertTopic.Publish → StreamUpdates case 3 实时推送）。
// 用 INSERT...RETURNING 取回完整 Alert 对象（含 id/ts），回调传完整对象。
func (s *Store) InsertAlert(ctx context.Context, ruleID *int64, level, message string) error {
	if s.alertPaused {
		return nil
	}
	q := `INSERT INTO alerts (rule_id, level, message) VALUES ($1, $2, $3)
		RETURNING id, ts, level, message, acked`
	var a Alert
	a.RuleID = ruleID
	err := s.pool.QueryRow(ctx, q, ruleID, level, message).Scan(&a.ID, &a.Ts, &a.Level, &a.Message, &a.Acked)
	if err != nil {
		return fmt.Errorf("insert alert: %w", err)
	}
	// D-114：写入后回调（main.go 注入 alertTopic.Publish → StreamUpdates case 3 实时推送）。
	// fire-and-forget：回调失败不影响写入（同 oppTopic 模式）。nil = 未接线（测试）。
	if s.alertHook != nil {
		s.alertHook(a)
	}
	return nil
}

// ListAlerts 分页返回告警（§8.2）。
func (s *Store) ListAlerts(ctx context.Context, limit, offset int) ([]Alert, error) {
	if limit <= 0 {
		limit = 100
	}
	q := `SELECT a.id, a.rule_id, COALESCE(r.name, ''), a.ts, a.level, a.message, a.acked
		FROM alerts a LEFT JOIN rules r ON a.rule_id = r.id
		ORDER BY a.ts DESC LIMIT $1 OFFSET $2`
	rows, err := s.pool.Query(ctx, q, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("list alerts: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.RuleID, &a.RuleName, &a.Ts, &a.Level, &a.Message, &a.Acked); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AckAlert 确认单条告警（幂等）。
func (s *Store) AckAlert(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE alerts SET acked = true WHERE id = $1`, id)
	return err
}

// AckAll 确认全部未读告警。
func (s *Store) AckAll(ctx context.Context) error {
	_, err := s.pool.Exec(ctx, `UPDATE alerts SET acked = true WHERE acked = false`)
	return err
}

// PurgeAckedAlerts 删除已确认且超期（默认 7 天）的告警（P5 修复⑧ TTL）。
// 幂等：每次调用执行同一 SQL，删除符合条件的行；未 acked 不删（待办语义）。
// 返回删除行数（用于日志/验收）。
func (s *Store) PurgeAckedAlerts(ctx context.Context, maxAge time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alerts WHERE acked = true AND ts < now() - $1::interval`, maxAge.String())
	if err != nil {
		return 0, fmt.Errorf("purge acked alerts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// DeleteAlerts 批量删除指定 ID 的告警（不管 acked 状态）。
func (s *Store) DeleteAlerts(ctx context.Context, ids []int64) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM alerts WHERE id = ANY($1)`, ids)
	if err != nil {
		return 0, fmt.Errorf("delete alerts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// AlertExistsByFragment 检查告警历史中是否已存在含 fragment 的行（D-265 事件盯梢去重）。
// fragment = 公告唯一标识（FileLink 或 URL），用于 LIKE 匹配。
// 永久去重（不加 ts 条件）：同一公告只推一次，已确认后不再重复推送。
func (s *Store) AlertExistsByFragment(ctx context.Context, fragment string) (bool, error) {
	var count int
	err := s.pool.QueryRow(ctx,
		`SELECT count(*) FROM alerts WHERE message LIKE '%' || $1 || '%'`,
		fragment).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("alert exists check: %w", err)
	}
	return count > 0, nil
}

// ClearAckedAlerts 删除所有已确认告警（不管时间）。
func (s *Store) ClearAckedAlerts(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM alerts WHERE acked = true`)
	if err != nil {
		return 0, fmt.Errorf("clear acked alerts: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListUnacked 返回未读告警 + 计数。
func (s *Store) ListUnacked(ctx context.Context) ([]Alert, int, error) {
	q := `SELECT a.id, a.rule_id, COALESCE(r.name, ''), a.ts, a.level, a.message, a.acked
		FROM alerts a LEFT JOIN rules r ON a.rule_id = r.id
		WHERE a.acked = false ORDER BY a.ts DESC`
	rows, err := s.pool.Query(ctx, q)
	if err != nil {
		return nil, 0, fmt.Errorf("list unacked: %w", err)
	}
	defer rows.Close()
	var out []Alert
	for rows.Next() {
		var a Alert
		if err := rows.Scan(&a.ID, &a.RuleID, &a.RuleName, &a.Ts, &a.Level, &a.Message, &a.Acked); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, err
	}
	return out, len(out), nil
}

func scanOpportunities(rows pgx.Rows) ([]Opportunity, error) {
	var out []Opportunity
	for rows.Next() {
		var o Opportunity
		if err := rows.Scan(
			&o.ID, &o.Type, &o.Canonical, &o.VenueBuy, &o.VenueSell, &o.PriceBuy, &o.PriceSell,
			&o.GrossBps, &o.SpreadCost, &o.CommissionCost, &o.SlippageCost, &o.SwapCost,
			&o.NetBps, &o.BreakevenDays, &o.QuoteTime, &o.ExpiresAt, &o.CreatedAt, &o.UpdatedAt,
		); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}
