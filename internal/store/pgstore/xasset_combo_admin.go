// xasset_combo_admin.go D-471 S3：xasset_allow_combos 管理面读/写（白名单不再裸 SQL）。
// ListAll 含禁用行（运维全量视图，区别于 LoadXAssetAllowCombos 的 enabled+env 过滤执行视图）；
// Update 走事务+行锁（approve 与 upsert 并发写同四元组时 reloader 不得读中间态，B-016）；
// 在持仓查询供 toggle 知情/caps 下调锁（B-020：新 caps < 在持名义→拒改）。
package pgstore

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// XAssetComboAdminRow 管理面行（含 id/enabled/simulated/updated_at，执行视图不读的列）。
type XAssetComboAdminRow struct {
	ID            int64
	CanonicalBuy  string
	CanonicalSell string
	VenueBuy      string
	VenueSell     string
	MaxLongQty    float64
	MaxShortQty   float64
	MaxGrossUSD   float64
	NotionalUSD   *float64
	MarginUSD     *float64 // D-010：保证金总预算（与 notional_usd 互斥，DB CHECK 钉死）
	Enabled       bool
	Simulated     *bool
	Note          string
}

// ListXAssetAllowCombosAll 全量行（含禁用；管理面渲染用，不过滤 env/enabled）。
// demo 域残留收管理面——simulated=t 死行不列（物理行留库=授权痕迹；
// 列可空 NULL 视同非 demo 行）。
func (s *Store) ListXAssetAllowCombosAll(ctx context.Context) ([]XAssetComboAdminRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, canonical_buy, canonical_sell, venue_key_buy AS venue_buy, venue_key_sell AS venue_sell,
		       max_long_qty, max_short_qty, max_gross_usd, notional_usd, margin_usd, enabled, simulated, note
		FROM xasset_allow_combos WHERE simulated IS NULL OR simulated = false ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("list xasset combos all: %w", err)
	}
	defer rows.Close()
	var out []XAssetComboAdminRow
	for rows.Next() {
		var r XAssetComboAdminRow
		if err := rows.Scan(&r.ID, &r.CanonicalBuy, &r.CanonicalSell, &r.VenueBuy, &r.VenueSell,
			&r.MaxLongQty, &r.MaxShortQty, &r.MaxGrossUSD, &r.NotionalUSD, &r.MarginUSD, &r.Enabled, &r.Simulated, &r.Note); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// UpdateXAssetAllowCombo 行锁更新（B-016）：tx 内 SELECT FOR UPDATE→UPDATE，并发写串行化。
// 返回 false=行不存在。
func (s *Store) UpdateXAssetAllowCombo(ctx context.Context, r *XAssetComboAdminRow) (bool, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback(ctx)
	var curID int64
	err = tx.QueryRow(ctx, `SELECT id FROM xasset_allow_combos WHERE id=$1 FOR UPDATE`, r.ID).Scan(&curID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return false, nil
		}
		return false, fmt.Errorf("lock combo row: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE xasset_allow_combos SET max_long_qty=$1, max_short_qty=$2, max_gross_usd=$3,
		       notional_usd=$4, margin_usd=$5, enabled=$6, note=$7, updated_at=now()
		WHERE id=$8`,
		r.MaxLongQty, r.MaxShortQty, r.MaxGrossUSD, r.NotionalUSD, r.MarginUSD, r.Enabled, r.Note, r.ID); err != nil {
		return false, fmt.Errorf("update combo: %w", err)
	}
	return true, tx.Commit(ctx)
}

// ListOpenPositionsForCombo 四元组命中的活跃持仓（B-016 toggle 知情 + B-020 caps 下调锁）。
// 匹配=双腿 venue 到位（BUY 腿=venue_buy、SELL 腿=venue_sell）且 dedup_key 含 canonical 段
// （dedup_key 形如 TYPE:canonical:vA_key:vB_key，XASSET 域内组合唯一性约束同型）。
// 返回（posIDs, 在持名义合计 notional_usd 和, error）。
func (s *Store) ListOpenPositionsForCombo(ctx context.Context, canonicalBuy, canonicalSell, venueBuy, venueSell string) ([]int64, float64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT p.id, COALESCE(p.notional_usd,0) FROM positions p
		WHERE p.status IN ('OPEN','LEGGED','CLOSING')
		  AND EXISTS (SELECT 1 FROM position_legs b WHERE b.position_id=p.id AND b.side='BUY' AND b.venue_key=$3)
		  AND EXISTS (SELECT 1 FROM position_legs sl WHERE sl.position_id=p.id AND sl.side='SELL' AND sl.venue_key=$4)
		  AND (p.dedup_key LIKE '%:'||$1||':%' OR p.dedup_key LIKE 'XASSET:'||$1||'%'
		       OR ($2 <> '' AND (p.dedup_key LIKE '%:'||$2||':%' OR p.dedup_key LIKE 'XASSET:'||$2||'%')))`,
		canonicalBuy, canonicalSell, venueBuy, venueSell)
	if err != nil {
		return nil, 0, fmt.Errorf("open positions for combo: %w", err)
	}
	defer rows.Close()
	var ids []int64
	var notional float64
	for rows.Next() {
		var id int64
		var n float64
		if err := rows.Scan(&id, &n); err != nil {
			return nil, 0, err
		}
		ids = append(ids, id)
		notional += n
	}
	return ids, notional, rows.Err()
}

// LatestTick 指定 (venue, venue_type, symbol) 最新 tick（ticks 表 venue_type 区分
// spot/perp/mt5，TicksWindow 不按 venue_type 过滤——dossier 价格源需精确到腿市场）。
// 无行→ok=false。
func (s *Store) LatestTick(ctx context.Context, venue, venueType, symbol string) (TickRow, bool, error) {
	var t TickRow
	err := s.pool.QueryRow(ctx, `
		SELECT ts, venue, symbol, bid, ask, venue_type FROM ticks
		WHERE venue=$1 AND venue_type=$2 AND symbol=$3
		ORDER BY ts DESC LIMIT 1`, venue, venueType, symbol).Scan(&t.Ts, &t.Venue, &t.Symbol, &t.Bid, &t.Ask, &t.VenueType)
	if err != nil {
		if err == pgx.ErrNoRows {
			return t, false, nil
		}
		return t, false, fmt.Errorf("latest tick: %w", err)
	}
	return t, true, nil
}

// XAssetMonitorSpreadP95 monitor 簿 entry_spread_bps p95（dossier 最坏场景锚）。
// 按 (cex_venue, mt_venue) 聚合（pair 列对通用 canonical 不可预知，venue 对粒度已足够）。
// 无样本→ok=false。
func (s *Store) XAssetMonitorSpreadP95(ctx context.Context, cexVenue, mtVenue string) (float64, bool, error) {
	var p95 *float64
	err := s.pool.QueryRow(ctx, `
		SELECT percentile_cont(0.95) WITHIN GROUP (ORDER BY entry_spread_bps)
		FROM fin_xasset_monitor WHERE cex_venue=$1 AND mt_venue=$2`, cexVenue, mtVenue).Scan(&p95)
	if err != nil {
		return 0, false, fmt.Errorf("monitor spread p95: %w", err)
	}
	if p95 == nil {
		return 0, false, nil
	}
	return *p95, true, nil
}
