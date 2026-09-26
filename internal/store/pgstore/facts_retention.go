// facts_retention.go —— facts 读面专用查询与 kind 级 TTL 治理（D-508 S3 拆件）。
package pgstore

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/fact"

	"github.com/jackc/pgx/v5"
)

// LatestFactsByKind D-508 S3：kind 单谓词无 OR 专用查询（热路径 kind-only 调用方专用，
// planner 命中 facts_kind_venue_symbol_ts_desc_idx；通用版保留给带参调用方）。
func (s *Store) LatestFactsByKind(ctx context.Context, kind string) ([]fact.Fact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (venue, symbol)
			kind, venue, COALESCE(symbol,'') AS symbol, value, unit, ts, src
		FROM facts
		WHERE kind = $1
		ORDER BY venue, symbol, ts DESC`, kind)
	if err != nil {
		return nil, fmt.Errorf("latest facts by kind: %w", err)
	}
	defer rows.Close()
	var out []fact.Fact
	for rows.Next() {
		var f fact.Fact
		if err := rows.Scan(&f.Kind, &f.Venue, &f.Symbol, &f.Value, &f.Unit, &f.Ts, &f.Src); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// factKindTTLDays D-508 S3 facts TTL 表（kind→保留天数）：accrue_settle 30d 回看+缓冲=35d；
// trade_mode/mt_symbol_meta=14d；opt_parity_dev 不入表（既有 7d 通道 PurgeOptParityFacts）。
var factKindTTLDays = map[string]int{
	"swap_long": 35, "swap_short": 35, "funding": 35, "funding_history": 35,
	"init_margin_rate": 35, "trade_mode": 14, "mt_symbol_meta": 14,
}

// factDefaultTTLDays 未列名 kind 默认保留窗。
const factDefaultTTLDays = 30

// PruneFacts D-508 S3：按 kind 窗删除超期 facts（挂既有 retention janitor 日轮）。
// 返回删除总行数（首轮行数记 LOG）。opt_parity_dev 不动（PruneOptParityFacts 专属通道）。
func (s *Store) PruneFacts(ctx context.Context) (int64, error) {
	var total int64
	named := make([]string, 0, len(factKindTTLDays))
	for kind, days := range factKindTTLDays {
		named = append(named, kind)
		tag, err := s.pool.Exec(ctx,
			`DELETE FROM facts WHERE kind = $1 AND ts < $2`,
			kind, time.Now().Add(-time.Duration(days)*24*time.Hour))
		if err != nil {
			return total, fmt.Errorf("prune facts kind=%s: %w", kind, err)
		}
		total += tag.RowsAffected()
	}
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM facts WHERE kind <> ALL($1) AND kind <> 'opt_parity_dev' AND ts < $2`,
		named, time.Now().Add(-factDefaultTTLDays*24*time.Hour))
	if err != nil {
		return total, fmt.Errorf("prune facts default window: %w", err)
	}
	return total + tag.RowsAffected(), nil
}

// PruneTicks D-508 S4b：ticks 长保窗 90d=分区级裁剪——保留当前月+前 3 个月分区，
// 更早分区 DETACH+DROP（分区级删除零表扫零膨胀，替代原 72h DELETE+VACUUM——74MB/日
// 实测下 90d≈6.7GB；72h 窗是 D-213 按 FX 单一读侧定的历史遗留）。返回 DROP 分区数；
// 首月边界无旧分区时优雅跳过（0,nil）。宿主 crontab ops_ticks_retention.sh 幂等保留。
func (s *Store) PruneTicks(ctx context.Context) (int64, error) {
	rows, err := s.pool.Query(ctx, `SELECT inhrelid::regclass::text FROM pg_inherits
		WHERE inhparent = 'ticks'::regclass`)
	if err != nil {
		return 0, fmt.Errorf("prune ticks list partitions: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return 0, fmt.Errorf("prune ticks scan: %w", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// 窗值=90d：本月首日回拨 3 个月为该月首日，更早分区整分区 DROP。
	now := time.Now().UTC()
	cutoff := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC).AddDate(0, -3, 0)
	var dropped int64
	for _, n := range names {
		var y, m int
		if _, err := fmt.Sscanf(n, "ticks_%d_%d", &y, &m); err != nil {
			continue // 非命名约定分区（人工件）不碰
		}
		if !time.Date(y, time.Month(m), 1, 0, 0, 0, 0, time.UTC).Before(cutoff) {
			continue
		}
		if _, err := s.pool.Exec(ctx, fmt.Sprintf(`ALTER TABLE ticks DETACH PARTITION %s`, pgx.Identifier{n}.Sanitize())); err != nil {
			return dropped, fmt.Errorf("detach %s: %w", n, err)
		}
		if _, err := s.pool.Exec(ctx, fmt.Sprintf(`DROP TABLE %s`, pgx.Identifier{n}.Sanitize())); err != nil {
			return dropped, fmt.Errorf("drop %s: %w", n, err)
		}
		dropped++
		log.Printf("[retention] ticks partition %s dropped (>90d)", n)
	}
	return dropped, nil
}
