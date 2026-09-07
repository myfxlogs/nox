package pgstore

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// SettlementRow position_settlements 行（D-087 §3.2，返修包② NotionalUsd 改 *float64）。
// NotionalUsd=nil → INSERT 写 NULL（skipped 行不伪造 0）。
// D-193-A：净 PNL 字段（PricePnlUsd/CashflowsNetUsd/NetRealizedUsd）+ 完整性状态字段。
type SettlementRow struct {
	PositionID    int64
	Vkey          string
	ExpectedUsd   float64
	RealizedUsd   float64  // 保留兼容（= price_pnl_usd 旧名）
	NotionalUsd   *float64 // nil → NULL（skipped 行不伪造 0）
	HoldDays      float64
	SkippedReason string
	SettledAt     time.Time
	// D-193-A 净 PNL 字段
	PricePnlUsd            *float64 // 价格差组件
	CashflowsNetUsd        *float64 // 现金流组件合计
	NetRealizedUsd         *float64 // 正式净 PNL
	PnlQuality             string   // actual/estimated/legacy/unknown
	CompletenessStatus     string   // complete/skipped/unknown/failed
	StatsStatus            string   // pending/applied/retryable/skipped
	StatsUpdatedAt         *time.Time
	SettlementModelVersion *int   // D-193-A = 1
	SettlementEventKey     string // 幂等键
}

// GetPosition 读单条持仓（D-087 settlement.Settle 用）。不存在→nil, nil。
// D-189 P0-4：含 closing_started_at + uncertainty_reason（reconcile 超时判定与 UNKNOWN 分流用）。
func (s *Store) GetPosition(ctx context.Context, id int64) (*Position, error) {
	q := `SELECT id, opportunity_id, type, status, COALESCE(expected_net_bps, 0), expected_entry_spread_bps, expected_swap_bps_daily, expected_annual_pct, realized_pnl, opened_at, closed_at, note, closing_started_at, uncertainty_reason
		FROM positions WHERE id = $1`
	var p Position
	err := s.pool.QueryRow(ctx, q, id).Scan(&p.ID, &p.OpportunityID, &p.Type, &p.Status, &p.ExpectedNetBps,
		&p.ExpectedEntrySpreadBps, &p.ExpectedSwapBpsDaily, &p.ExpectedAnnualPct,
		&p.RealizedPnl, &p.OpenedAt, &p.ClosedAt, &p.Note, &p.ClosingStartedAt, &p.UncertaintyReason)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get position %d: %w", id, err)
	}
	return &p, nil
}

// InsertSettlement 写 position_settlements 行（D-087 §3.2，返修包②并发安全）。
// 返回 won=true 表示本次写入生效（INSERT 新行 或 UPDATE 升级了非 applied 行）；
// won=false 表示 ON CONFLICT 命中但已是 applied（输家，不重写）。
// 调用方（Settle）输家直接 return nil，不再走步骤 7/8，防并发双计 verification 样本+strategy_stats。
// D-193-A：写入净 PNL + 完整性状态字段（stats_status 直接写最终值，无二次 UPDATE）。
// D-249 S4 批3 第2项：ON CONFLICT DO UPDATE WHERE stats_status<>'applied'——
// 消除审计 P1-2 墓碑行（首写 retryable 后重试永远 won=false 静默跳过统计的升级路径缺失）。
// xmax=0 表示 INSERT（新行，赢家）；xmax<>0 且 WHERE 命中表示 UPDATE（升级，赢家）；
// xmax<>0 且 WHERE 未命中（已是 applied）→ pgx.ErrNoRows（输家）。
func (s *Store) InsertSettlement(ctx context.Context, r *SettlementRow) (won bool, err error) {
	var posID int64
	err = s.pool.QueryRow(ctx, `
		INSERT INTO position_settlements
			(position_id, vkey, expected_usd, realized_usd, notional_usd, hold_days, skipped_reason, settled_at,
			 price_pnl_usd, cashflows_net_usd, net_realized_usd, pnl_quality, completeness_status,
			 stats_status, settlement_model_version, settlement_event_key)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16)
		ON CONFLICT (position_id) DO UPDATE SET
				vkey = EXCLUDED.vkey,
				expected_usd = EXCLUDED.expected_usd,
				realized_usd = EXCLUDED.realized_usd,
				notional_usd = EXCLUDED.notional_usd,
				hold_days = EXCLUDED.hold_days,
				skipped_reason = EXCLUDED.skipped_reason,
				settled_at = EXCLUDED.settled_at,
				price_pnl_usd = EXCLUDED.price_pnl_usd,
				cashflows_net_usd = EXCLUDED.cashflows_net_usd,
				net_realized_usd = EXCLUDED.net_realized_usd,
				pnl_quality = EXCLUDED.pnl_quality,
				completeness_status = EXCLUDED.completeness_status,
				stats_status = EXCLUDED.stats_status,
				settlement_model_version = EXCLUDED.settlement_model_version,
				settlement_event_key = EXCLUDED.settlement_event_key
			WHERE position_settlements.stats_status <> 'applied'
		RETURNING position_id
	`, r.PositionID, r.Vkey, r.ExpectedUsd, r.RealizedUsd, r.NotionalUsd, r.HoldDays, r.SkippedReason, r.SettledAt,
		r.PricePnlUsd, r.CashflowsNetUsd, r.NetRealizedUsd, nullableText(r.PnlQuality), nullableText(r.CompletenessStatus),
		nullableText(r.StatsStatus), r.SettlementModelVersion, nullableText(r.SettlementEventKey)).Scan(&posID)
	// r.NotionalUsd is *float64: nil → NULL (pgx native), non-nil → value
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return false, nil // ON CONFLICT 命中但已是 applied（输家，不重写）
		}
		return false, fmt.Errorf("insert settlement pos=%d: %w", r.PositionID, err)
	}
	return true, nil
}

// nullableText 空字符串 → nil（写 NULL），非空 → 值。D-193-A：settlement 文本字段容错。
func nullableText(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// HasSettlement 查 position_settlements 是否已有行（D-087 幂等短路）。
func (s *Store) HasSettlement(ctx context.Context, posID int64) (bool, error) {
	var one int
	err := s.pool.QueryRow(ctx, `SELECT 1 FROM position_settlements WHERE position_id = $1`, posID).Scan(&one)
	if err == nil {
		return true, nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return false, fmt.Errorf("check settlement pos=%d: %w", posID, err)
}

// GetSettlementByPos D-091 分析员 get_entity：读单条 position_settlements 行。不存在→nil, nil。
// D-193-A：扫描净 PNL + 完整性状态字段（指针/COALESCE 容错 legacy 行 NULL）。
func (s *Store) GetSettlementByPos(ctx context.Context, posID int64) (*SettlementRow, error) {
	var r SettlementRow
	var pnlQuality, completeness, statsStatus, eventKey *string
	err := s.pool.QueryRow(ctx, `
		SELECT position_id, vkey, expected_usd, realized_usd, notional_usd, hold_days, COALESCE(skipped_reason, ''), settled_at,
		       price_pnl_usd, cashflows_net_usd, net_realized_usd, pnl_quality, completeness_status,
		       stats_status, stats_updated_at, settlement_model_version, settlement_event_key
		FROM position_settlements WHERE position_id = $1`, posID).
		Scan(&r.PositionID, &r.Vkey, &r.ExpectedUsd, &r.RealizedUsd, &r.NotionalUsd, &r.HoldDays, &r.SkippedReason, &r.SettledAt,
			&r.PricePnlUsd, &r.CashflowsNetUsd, &r.NetRealizedUsd, &pnlQuality, &completeness,
			&statsStatus, &r.StatsUpdatedAt, &r.SettlementModelVersion, &eventKey)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("get settlement pos=%d: %w", posID, err)
	}
	r.PnlQuality = ptrStr(pnlQuality)
	r.CompletenessStatus = ptrStr(completeness)
	r.StatsStatus = ptrStr(statsStatus)
	r.SettlementEventKey = ptrStr(eventKey)
	return &r, nil
}

// ptrStr *string → string（nil → ""）。
func ptrStr(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// ListSettlementsByPosIDs D-106：按 position_id 批量查结算行（ListPositions join 用，单条 SQL IN 防 N+1）。
// posIDs 空 → 返回空 map（不查库）。返回 map[posID]*SettlementRow；无结算行的 posID 不在 map 中。
// D-193-A：扫描净 PNL + 完整性状态字段（指针/COALESCE 容错 legacy 行 NULL）。
func (s *Store) ListSettlementsByPosIDs(ctx context.Context, posIDs []int64) (map[int64]*SettlementRow, error) {
	out := make(map[int64]*SettlementRow, len(posIDs))
	if len(posIDs) == 0 {
		return out, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT position_id, vkey, expected_usd, realized_usd, notional_usd, hold_days, COALESCE(skipped_reason, ''), settled_at,
		       price_pnl_usd, cashflows_net_usd, net_realized_usd, pnl_quality, completeness_status,
		       stats_status, stats_updated_at, settlement_model_version, settlement_event_key
		FROM position_settlements WHERE position_id = ANY($1)`, posIDs)
	if err != nil {
		return nil, fmt.Errorf("list settlements by pos ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var r SettlementRow
		var pnlQuality, completeness, statsStatus, eventKey *string
		if err := rows.Scan(&r.PositionID, &r.Vkey, &r.ExpectedUsd, &r.RealizedUsd, &r.NotionalUsd, &r.HoldDays, &r.SkippedReason, &r.SettledAt,
			&r.PricePnlUsd, &r.CashflowsNetUsd, &r.NetRealizedUsd, &pnlQuality, &completeness,
			&statsStatus, &r.StatsUpdatedAt, &r.SettlementModelVersion, &eventKey); err != nil {
			return nil, err
		}
		r.PnlQuality = ptrStr(pnlQuality)
		r.CompletenessStatus = ptrStr(completeness)
		r.StatsStatus = ptrStr(statsStatus)
		r.SettlementEventKey = ptrStr(eventKey)
		out[r.PositionID] = &r
	}
	return out, rows.Err()
}

// GetSettlementStatsStatus D-193-A：查 settlement 行的 stats_status（短路判定用）。
// 不存在行 → 返回 ("", nil)（继续走 Settle 流程）。
// 存在行 → 返回 stats_status（applied/skipped 短路；pending/retryable/"" 继续）。
func (s *Store) GetSettlementStatsStatus(ctx context.Context, posID int64) (string, error) {
	var status *string
	err := s.pool.QueryRow(ctx,
		`SELECT stats_status FROM position_settlements WHERE position_id=$1`, posID).Scan(&status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil // 无行，继续 Settle
		}
		return "", fmt.Errorf("get settlement stats status pos=%d: %w", posID, err)
	}
	if status == nil {
		return "", nil // legacy 行无 stats_status，继续 Settle（兼容）
	}
	return *status, nil
}

// MarkSettlementStatsRetryable D-193 审计 P0-2：统计更新失败时改 stats_status=retryable，下轮重试。
func (s *Store) MarkSettlementStatsRetryable(ctx context.Context, posID int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE position_settlements SET stats_status='retryable', stats_updated_at=now() WHERE position_id=$1`,
		posID)
	return err
}
