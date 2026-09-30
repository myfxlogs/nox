// canonical_exposure_caps.go D-006 S3：fin_canonical_exposure_caps 读写面 +
// canonical 聚簇敞口判定（venue_exposure_caps.go 同构）。
// 三态语义：查无行→默认 defaultCanonicalCapUSD；enabled=false→闸停用不禁；
// enabled+cap_usd=0→冻结新入场。
package pgstore

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// DefaultCanonicalCapUSD D-006 SSOT：canonical 聚簇敞口默认上限（无行时用）。
const DefaultCanonicalCapUSD = 6000.0

// CanonicalExposureCap canonical 级聚簇敞口上限。
type CanonicalExposureCap struct {
	Canonical string
	CapUSD    float64
	Enabled   bool
	Note      string
	UpdatedAt time.Time
}

// ListCanonicalExposureCaps 全量载入 map 消费。
func (s *Store) ListCanonicalExposureCaps(ctx context.Context) (map[string]CanonicalExposureCap, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT canonical, cap_usd, enabled, note, updated_at FROM fin_canonical_exposure_caps`)
	if err != nil {
		return nil, fmt.Errorf("list canonical exposure caps: %w", err)
	}
	defer rows.Close()
	out := map[string]CanonicalExposureCap{}
	for rows.Next() {
		var c CanonicalExposureCap
		if err := rows.Scan(&c.Canonical, &c.CapUSD, &c.Enabled, &c.Note, &c.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan canonical exposure cap: %w", err)
		}
		out[c.Canonical] = c
	}
	return out, rows.Err()
}

// UpsertCanonicalExposureCap 写/改闸（负 cap 由 CHECK 拒）。
func (s *Store) UpsertCanonicalExposureCap(ctx context.Context, c *CanonicalExposureCap) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fin_canonical_exposure_caps (canonical, cap_usd, enabled, note)
		VALUES ($1,$2,$3,$4)
		ON CONFLICT (canonical) DO UPDATE SET cap_usd=$2, enabled=$3, note=$4, updated_at=now()`,
		c.Canonical, c.CapUSD, c.Enabled, c.Note)
	if err != nil {
		return fmt.Errorf("upsert canonical exposure cap %s: %w", c.Canonical, err)
	}
	return nil
}

// sumOpenCanonicalNotionalTx canonical 名下在途仓（OPENING/OPEN/LEGGED/CLOSING）
// 的 notional_usd 合计。canonical 归属双源：①腿行 broker_symbol_map 映射
// （未命中按 leg.symbol 原值，CEX 腿原生 canonical 名义）；②守卫建库事务内落的
// fin_position_canonicals 投影（同窗兄弟仓腿未落时可见——TOCTOU 收口）。
// NULL notional 不计；按 p.id 去重（SUM(DISTINCT notional) 同值塌缩坑）。
func sumOpenCanonicalNotionalTx(ctx context.Context, q pgx.Tx, canonical string) (float64, error) {
	var sum float64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(sub.notional_usd), 0) FROM (
			SELECT DISTINCT p.id, p.notional_usd
			FROM positions p
			WHERE p.status IN ('OPENING','OPEN','LEGGED','CLOSING')
			  AND p.notional_usd IS NOT NULL
			  AND (
				EXISTS (
					SELECT 1 FROM position_legs l
					LEFT JOIN broker_symbol_map m
					  ON m.venue = split_part(l.venue_key, ':', 1) AND m.broker_symbol = l.symbol
					WHERE l.position_id = p.id
					  AND COALESCE(NULLIF(m.canonical,''), l.symbol) = $1
				)
				OR EXISTS (
					SELECT 1 FROM fin_position_canonicals g
					WHERE g.position_id = p.id AND g.canonical = $1
				)
			  )
		) sub`, canonical).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum open canonical notional %s: %w", canonical, err)
	}
	return sum, nil
}

// SumOpenCanonicalNotional 非事务版（probeGates/engine 判点用）。
func (s *Store) SumOpenCanonicalNotional(ctx context.Context, canonical string) (float64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback(ctx)
	return sumOpenCanonicalNotionalTx(ctx, tx, canonical)
}

// CanonicalCapReject D-006 S3：聚簇前瞻判据——Σ在途同 canonical 名义+本次名义 > cap → 拒。
// 返回 ""=放行；非空=拒因文案。canonical 空串不判。查询失败=fail-closed 拒因。
func (s *Store) CanonicalCapReject(ctx context.Context, canonical string, proposedUSD float64) string {
	if canonical == "" {
		return ""
	}
	caps, err := s.ListCanonicalExposureCaps(ctx)
	if err != nil {
		return fmt.Sprintf("canonical 敞口闸表读取失败: %v（fail-closed 不放行）", err)
	}
	if row, ok := caps[canonical]; ok && !row.Enabled {
		return "" // 闸停用
	}
	capUSD := DefaultCanonicalCapUSD
	if row, ok := caps[canonical]; ok {
		capUSD = row.CapUSD
	}
	if capUSD == 0 {
		return fmt.Sprintf("canonical 聚簇冻结：%s cap=0", canonical)
	}
	sum, err := s.SumOpenCanonicalNotional(ctx, canonical)
	if err != nil {
		return fmt.Sprintf("canonical 聚簇敞口查询失败（保守不放行）: %s %v", canonical, err)
	}
	if sum+proposedUSD > capUSD {
		return fmt.Sprintf("canonical 聚簇超限：%s 在途 $%.0f+本次 $%.0f > cap $%.0f", canonical, sum, proposedUSD, capUSD)
	}
	return ""
}

// CreatePositionWithCapGuard D-006 S3 TOCTOU：advisory 锁内复查 canonical 聚簇 cap
// 后原子建 position——两笔同窗口放行时第二笔串行到锁后复查被拦。
// lockKey=venue_buy|canonical 复合（同 canonical 不同 venue 组合同锁，聚簇口径正确）。
func (s *Store) CreatePositionWithCapGuard(ctx context.Context, p *Position, canonicals []string, proposedUSD float64) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("create position guarded (begin): %w", err)
	}
	defer tx.Rollback(ctx) // commit 后 no-op
	// 去重 canonical 集（同标的组合买=卖只判一次）。
	seen := map[string]bool{}
	var uniq []string
	for _, c := range canonicals {
		if c != "" && !seen[c] {
			seen[c] = true
			uniq = append(uniq, c)
		}
	}
	for _, c := range uniq {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext($1))`, c); err != nil {
			return 0, fmt.Errorf("canonical cap lock %s: %w", c, err)
		}
	}
	for _, c := range uniq {
		capUSD := DefaultCanonicalCapUSD
		var enabled bool
		var rowCap float64
		err := tx.QueryRow(ctx,
			`SELECT cap_usd, enabled FROM fin_canonical_exposure_caps WHERE canonical=$1`, c).
			Scan(&rowCap, &enabled)
		if err != nil && err != pgx.ErrNoRows {
			return 0, fmt.Errorf("canonical cap recheck %s: %w", c, err)
		}
		if err == nil {
			if !enabled {
				continue // 闸停用
			}
			capUSD = rowCap
		}
		if capUSD == 0 {
			return 0, fmt.Errorf("canonical 聚簇冻结：%s cap=0", c)
		}
		sum, err := sumOpenCanonicalNotionalTx(ctx, tx, c)
		if err != nil {
			return 0, err
		}
		if sum+proposedUSD > capUSD {
			return 0, fmt.Errorf("canonical 聚簇超限：%s 在途 $%.0f+本次 $%.0f > cap $%.0f", c, sum, proposedUSD, capUSD)
		}
	}
	// 同窗兄弟仓可见性：position 携带 proposed 名义 + fin_position_canonicals 归属投影
	// 同事务落库——第二笔锁后复查即见（腿行在 persistOpenLegs 后落，纯腿归属会漏）。
	if p.NotionalUSD == nil {
		p.NotionalUSD = &proposedUSD
	}
	id, err := createPositionInTx(ctx, tx, p)
	if err != nil {
		return 0, err
	}
	for _, c := range uniq {
		if _, err := tx.Exec(ctx,
			`INSERT INTO fin_position_canonicals (position_id, canonical) VALUES ($1,$2)
			 ON CONFLICT DO NOTHING`, id, c); err != nil {
			return 0, fmt.Errorf("canonical guard row %s: %w", c, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("create position guarded (commit): %w", err)
	}
	s.notifyPositionChange()
	return id, nil
}

// SumOpenNotionalUSDByVenue D-006 S2：该 venue 名下在途仓 notional_usd 合计
// （FreeMargin 双因子告警的 Σnotional 输入）。venue 按 position_legs.venue_key
// 裸账户段匹配；NULL notional 仓不计。
func (s *Store) SumOpenNotionalUSDByVenue(ctx context.Context, venue string) (float64, error) {
	var sum float64
	err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(SUM(sub.notional_usd), 0) FROM (
			SELECT DISTINCT p.id, p.notional_usd
			FROM positions p
			WHERE p.status IN ('OPENING','OPEN','LEGGED','CLOSING')
			  AND p.notional_usd IS NOT NULL
			  AND EXISTS (
				SELECT 1 FROM position_legs l
				WHERE l.position_id = p.id AND split_part(l.venue_key, ':', 1) = $1
			  )
		) sub`, venue).Scan(&sum)
	if err != nil {
		return 0, fmt.Errorf("sum open notional venue %s: %w", venue, err)
	}
	return sum, nil
}
