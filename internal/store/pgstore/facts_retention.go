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

// factKindTTLDays facts TTL 表（kind→保留天数）。D-508 S3 立档：accrue_settle 30d 回看
// +缓冲=35d（funding/swap 族历史行承重，勿降）；trade_mode/mt_symbol_meta=14d；
// opt_parity_dev 不入表（既有 7d 通道 PurgeOptParityFacts）。
// D-108② 扩档（票内举证=SG 实拍 664.5 万行/2249MB，行龄逐 kind 对齐现窗证明 prune 在跑，
// 根因=只消费最新值的超高频 kind 挂 30d 缺省窗）：funding_next_settle 130 万行/13 天
// （消费=倒计时/funding_due，8h 结算网格 2d 足矣）、funding_interval 85 万行/13 天
// （periods/day，2d）、index_price 21.8 万行/7 天（10min 节流，radar 读窗 15min→2d）、
// vol24h 7 万行/1 天（15min 节流，radar 小时读→2d）、trade_session 17.5 万行/4 天
// （消费=当周会话表→7d）。
var factKindTTLDays = map[string]int{
	"swap_long": 35, "swap_short": 35, "funding": 35, "funding_history": 35,
	"init_margin_rate": 35, "trade_mode": 14, "mt_symbol_meta": 14,
	"funding_next_settle": 2, "funding_interval": 2, "index_price": 2, "vol24h": 2,
	"trade_session": 7,
}

// factDefaultTTLDays 未列名 kind 默认保留窗。
const factDefaultTTLDays = 30

// factPruneBatch D-108②：单条 DELETE 批量上限（十万级批——一次性回填 funding_next_settle
// ~129 万行时分批落锁，防 4C/7G 共享实例长事务/WAL 尖峰）。
const factPruneBatch = 100000

// pruneFactsKind 单 kind 分批删：ts 窗外且非该组终值快照（组=kind+venue_type+venue+symbol，
// D-108② 终值快照永存——EXISTS 更新兄弟行=非组内最新才删；索引 facts_kind_venue_symbol_ts_desc_idx
// 服 probe）。批量循环至清零。
func (s *Store) pruneFactsKind(ctx context.Context, kind string, cutoff time.Time) (int64, error) {
	var total int64
	for {
		tag, err := s.pool.Exec(ctx, `
			DELETE FROM facts WHERE ctid IN (
				SELECT f.ctid FROM facts f
				WHERE f.kind = $1 AND f.ts < $2
				  AND EXISTS (SELECT 1 FROM facts g
				      WHERE g.kind = f.kind AND g.venue_type = f.venue_type
				        AND g.venue = f.venue AND g.symbol IS NOT DISTINCT FROM f.symbol
				        AND g.ts > f.ts)
				LIMIT $3
			)`, kind, cutoff, factPruneBatch)
		if err != nil {
			return total, err
		}
		total += tag.RowsAffected()
		if tag.RowsAffected() < factPruneBatch {
			return total, nil
		}
	}
}

// PruneFacts D-508 S3：按 kind 窗删除超期 facts（挂既有 retention janitor 日轮）。
// D-108②：每 (kind,venue_type,venue,symbol) 组终值快照永存（pruneFactsKind EXISTS 卫兵
// ——停发符号的末读数不随窗消失）+ 分批删除（factPruneBatch）；部署后首个 janitor 轮
// 自动回填存量超窗行，免人工清库。返回删除总行数。opt_parity_dev 不动（专属 7d 通道）。
func (s *Store) PruneFacts(ctx context.Context) (int64, error) {
	var total int64
	named := make([]string, 0, len(factKindTTLDays))
	for kind, days := range factKindTTLDays {
		named = append(named, kind)
		n, err := s.pruneFactsKind(ctx, kind, time.Now().Add(-time.Duration(days)*24*time.Hour))
		if err != nil {
			return total, fmt.Errorf("prune facts kind=%s: %w", kind, err)
		}
		total += n
	}
	// 未列名 kind 缺省窗（终值卫兵同规则；低频 kind 行龄长=组终值被卫兵留住）。
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM facts f WHERE kind <> ALL($1) AND kind <> 'opt_parity_dev' AND ts < $2
		 AND EXISTS (SELECT 1 FROM facts g
		     WHERE g.kind = f.kind AND g.venue_type = f.venue_type
		       AND g.venue = f.venue AND g.symbol IS NOT DISTINCT FROM f.symbol
		     AND g.ts > f.ts)`,
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
