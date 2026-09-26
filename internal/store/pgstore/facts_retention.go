// facts_retention.go —— facts 读面专用查询与 kind 级 TTL 治理（D-508 S3 拆件）。
package pgstore

import (
	"context"
	"fmt"
	"log"
	"time"

	"arb/internal/fact"
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

// PruneTicks D-508 S4：ticks 72h retention 入应用（原 ops_ticks_retention.sh cron 逻辑；
// 唯一读侧 USDPer 七直盘 24h 窗，72h=3 倍余量）。DELETE 返回删行数；VACUUM ANALYZE
// 防死元组堆积（独立于 DELETE 成败各自留痕）。
func (s *Store) PruneTicks(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM ticks WHERE ts < now() - interval '72 hours'`)
	if err != nil {
		return 0, fmt.Errorf("prune ticks: %w", err)
	}
	if _, err := s.pool.Exec(ctx, `VACUUM ANALYZE ticks`); err != nil {
		log.Printf("[retention] ticks vacuum: %v", err) // 留痕不吞；DELETE 成果不因此作废
	}
	return tag.RowsAffected(), nil
}
